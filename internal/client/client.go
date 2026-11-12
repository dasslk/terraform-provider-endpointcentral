// Package client is a small HTTP client for the ManageEngine Endpoint Central
// Cloud REST API.
//
// The API surface comes in two flavours that behave differently:
//
//   - The legacy "/api/1.4/..." endpoints wrap every payload in an envelope
//     ({"status": "success", "message_response": {...}}) and may report
//     failures with HTTP 200/204 and {"status": "error", "error_code": ...}.
//   - The newer "/dcapi/..." and "/emsapi/..." endpoints use vendor media types
//     in the Accept / Content-Type headers (e.g. application/ipScopeDetail.v1+json)
//     and report failures through HTTP status codes with
//     {"statusCode": ..., "errorCode": ..., "message": ...}.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultMaxRetries is the number of retries when none is configured.
const DefaultMaxRetries = 4

// Client talks to a single Endpoint Central Cloud tenant.
type Client struct {
	BaseURL string
	// MDMBaseURL is the host of the MDM API (mdm.manageengine.<region>).
	MDMBaseURL string
	HTTPClient *http.Client
	Tokens     TokenSource
	UserAgent  string
	// MaxRetries is how often a request is retried after a throttling (429)
	// or server (5xx) error or a network failure. 0 means DefaultMaxRetries,
	// a negative value disables retries.
	MaxRetries int

	groupLocks sync.Map // custom group ID -> *sync.Mutex, see lockGroup
}

// Request describes one API call.
type Request struct {
	Method string
	Path   string
	Query  url.Values
	// Body is JSON-encoded when non-nil.
	Body any
	// RawBody is sent as is, with ContentType, instead of Body (e.g. a
	// multipart upload).
	RawBody []byte
	// Accept and ContentType default to application/json. The dcapi endpoints
	// require vendor media types such as application/ipScopeDetail.v1+json.
	Accept      string
	ContentType string
	// Headers are extra request headers.
	Headers map[string]string
	// NoServerErrorRetry skips the retries after a 5xx, for calls that fail
	// with a 5xx deterministically and have a fallback.
	NoServerErrorRetry bool
	// MDM sends the request to the MDM API host instead of BaseURL.
	MDM bool
}

// APIError is returned for any non-successful API response.
type APIError struct {
	HTTPStatus int
	Code       string
	Message    string
	Body       string
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = strings.TrimSpace(e.Body)
		if len(msg) > 500 {
			msg = msg[:500] + "..."
		}
	}
	if e.Code != "" {
		return fmt.Sprintf("endpoint central API error (HTTP %d, code %s): %s", e.HTTPStatus, e.Code, msg)
	}
	return fmt.Sprintf("endpoint central API error (HTTP %d): %s", e.HTTPStatus, msg)
}

// IsNotFound reports whether err represents a missing object.
// isPermissionDenial reports a 401 that refuses the request itself rather
// than the token: a scope the call does not accept, or Endpoint Central's
// "Access denied" (errorCode 1001, also what a deleted configuration answers).
func isPermissionDenial(raw []byte) bool {
	e := parseError(http.StatusUnauthorized, raw)
	return e.Code == "1001" || strings.Contains(e.Message, "INVALID_OAUTHSCOPE")
}

func IsNotFound(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) {
		return false
	}
	if ae.HTTPStatus == http.StatusNotFound {
		return true
	}
	// Legacy endpoints answer "does not exist" with 200/204 + status=error.
	return strings.Contains(strings.ToLower(ae.Message), "does not exist") ||
		strings.Contains(strings.ToLower(ae.Message), "not found")
}

// Do executes the request and decodes a JSON response into out (if non-nil).
// It returns the raw response body as well, which is useful for envelope parsing.
func (c *Client) Do(ctx context.Context, r Request, out any) error {
	raw, err := c.do(ctx, r)
	if err != nil {
		return err
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decoding response of %s %s: %w", r.Method, r.Path, err)
	}
	return nil
}

func (c *Client) do(ctx context.Context, r Request) ([]byte, error) {
	payload := r.RawBody
	if payload == nil && r.Body != nil {
		b, err := json.Marshal(r.Body)
		if err != nil {
			return nil, fmt.Errorf("encoding request body: %w", err)
		}
		payload = b
	}

	base := c.BaseURL
	if r.MDM {
		base = c.MDMBaseURL
	}
	u := strings.TrimRight(base, "/") + r.Path
	if len(r.Query) > 0 {
		u += "?" + r.Query.Encode()
	}

	hc := c.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	maxRetries := c.MaxRetries
	switch {
	case maxRetries == 0:
		maxRetries = DefaultMaxRetries
	case maxRetries < 0:
		maxRetries = 0 // retries disabled
	}

	reauthed := false
	for attempt := 0; ; attempt++ {
		token, err := c.Tokens.Token(ctx)
		if err != nil {
			return nil, err
		}

		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, r.Method, u, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Zoho-oauthtoken "+token)
		req.Header.Set("Accept", orDefault(r.Accept, "application/json"))
		if payload != nil {
			req.Header.Set("Content-Type", orDefault(r.ContentType, "application/json"))
		}
		if c.UserAgent != "" {
			req.Header.Set("User-Agent", c.UserAgent)
		}
		for k, v := range r.Headers {
			req.Header.Set(k, v)
		}

		resp, err := hc.Do(req)
		if err != nil {
			if attempt < maxRetries && ctx.Err() == nil {
				if werr := sleep(ctx, backoff(attempt, "")); werr != nil {
					return nil, werr
				}
				continue
			}
			return nil, fmt.Errorf("%s %s: %w", r.Method, r.Path, err)
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		_ = resp.Body.Close()

		switch {
		case resp.StatusCode == http.StatusUnauthorized && !reauthed && !isPermissionDenial(raw):
			// Token may have been revoked or expired early; fetch a fresh one once.
			// Not for a denial (missing scope, no access to the object): a new
			// token changes nothing there, and Zoho rate-limits new tokens.
			c.Tokens.Invalidate()
			reauthed = true
			continue
		case (resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode >= 500 && !r.NoServerErrorRetry)) && attempt < maxRetries:
			if werr := sleep(ctx, backoff(attempt, resp.Header.Get("Retry-After"))); werr != nil {
				return nil, werr
			}
			continue
		}

		if resp.StatusCode >= 300 {
			return nil, parseError(resp.StatusCode, raw)
		}
		if apiErr := envelopeError(resp.StatusCode, raw); apiErr != nil {
			return nil, apiErr
		}
		return raw, nil
	}
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func backoff(attempt int, retryAfter string) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}
	d := time.Duration(1<<attempt) * 500 * time.Millisecond
	return d + time.Duration(rand.Int64N(int64(250*time.Millisecond)))
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// errorBody is the union of the two error formats the API uses.
type errorBody struct {
	// dcapi style
	StatusCode FlexString `json:"statusCode"`
	ErrorCode  FlexString `json:"errorCode"`
	Message    string     `json:"message"`
	ErrorMsg   string     `json:"errorMsg"`
	// legacy 1.4 style
	Status           string     `json:"status"`
	ErrorCodeLegacy  FlexString `json:"error_code"`
	ErrorDescription string     `json:"error_description"`
}

func parseError(status int, raw []byte) *APIError {
	e := &APIError{HTTPStatus: status, Body: string(raw)}
	var eb errorBody
	if json.Unmarshal(raw, &eb) == nil {
		e.Code = string(eb.ErrorCode)
		if e.Code == "" {
			e.Code = string(eb.ErrorCodeLegacy)
		}
		e.Message = eb.Message
		if e.Message == "" {
			e.Message = eb.ErrorMsg
		}
		if e.Message == "" {
			e.Message = eb.ErrorDescription
		}
	}
	return e
}

// envelopeError detects failures reported inside a successful HTTP response.
func envelopeError(status int, raw []byte) *APIError {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	var eb errorBody
	if err := json.Unmarshal(trimmed, &eb); err != nil {
		return nil //nolint:nilerr // a body that is not an envelope carries no error
	}
	if strings.EqualFold(eb.Status, "error") || strings.EqualFold(eb.Status, "failure") {
		return parseError(status, raw)
	}
	return nil
}

// FlexString unmarshals from either a JSON string or a JSON number. The API is
// inconsistent about whether IDs are encoded as strings or numbers.
type FlexString string

func (f *FlexString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*f = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = FlexString(s)
		return nil
	}
	*f = FlexString(string(b))
	return nil
}

func (f FlexString) String() string { return string(f) }

// Int64 parses the value as an integer, returning 0 if it is empty or invalid.
func (f FlexString) Int64() int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(string(f)), 10, 64)
	return n
}

// lastPage decides whether a legacy paginated listing is exhausted. The total
// is preferred because the server may cap the page size below what was asked.
func lastPage(batchLen, seen, total, limit int) bool {
	if batchLen == 0 {
		return true
	}
	if total > 0 {
		return seen >= total
	}
	return batchLen < limit
}
