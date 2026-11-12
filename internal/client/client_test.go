package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRefreshTokenSource_cachesAndRefreshes(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/oauth/v2/token" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("client_id") != "cid" {
			t.Errorf("unexpected form %v", r.Form)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	}))
	defer srv.Close()

	now := time.Unix(1_700_000_000, 0)
	ts := &RefreshTokenSource{AccountsURL: srv.URL, ClientID: "cid", ClientSecret: "s", RefreshToken: "r", now: func() time.Time { return now }}
	for range 3 {
		if tok, err := ts.Token(context.Background()); err != nil || tok != "tok" {
			t.Fatalf("Token() = %q, %v", tok, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("expected token to be cached, got %d calls", calls.Load())
	}
	now = now.Add(59 * time.Minute) // within the expiry leeway
	_, _ = ts.Token(context.Background())
	if calls.Load() != 2 {
		t.Fatalf("expected refresh near expiry, got %d calls", calls.Load())
	}
}

func TestRefreshTokenSource_rateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// What Zoho answered on a live tenant after many token requests.
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "Access Denied", "status": "failure",
			"error_description": "You have made too many requests continuously. Please try again after some time."})
	}))
	defer srv.Close()
	_, err := (&RefreshTokenSource{AccountsURL: srv.URL}).Token(context.Background())
	if err == nil || !strings.Contains(err.Error(), "rate-limiting") || strings.Contains(err.Error(), "check client_id") {
		t.Fatalf("got %v", err)
	}
}

func TestRefreshTokenSource_errorFieldOn200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_client"})
	}))
	defer srv.Close()
	ts := &RefreshTokenSource{AccountsURL: srv.URL}
	if _, err := ts.Token(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

type countingTokens struct{ invalidated atomic.Int32 }

func (c *countingTokens) Token(context.Context) (string, error) {
	if c.invalidated.Load() > 0 {
		return "fresh", nil
	}
	return "stale", nil
}
func (c *countingTokens) Invalidate() { c.invalidated.Add(1) }

func TestDo_retriesOnceAfter401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Zoho-oauthtoken fresh" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","message_response":{"x":1}}`))
	}))
	defer srv.Close()
	tokens := &countingTokens{}
	c := &Client{BaseURL: srv.URL, Tokens: tokens}
	if err := c.Do(context.Background(), Request{Method: http.MethodGet, Path: "/x"}, nil); err != nil {
		t.Fatal(err)
	}
	if tokens.invalidated.Load() != 1 {
		t.Fatalf("expected one invalidation, got %d", tokens.invalidated.Load())
	}
}

func TestDo_errorFormats(t *testing.T) {
	cases := map[string]struct {
		status   int
		body     string
		code     string
		notFound bool
	}{
		"legacy envelope on 200": {200, `{"status":"error","error_code":"20015","error_description":"Foo does not exist"}`, "20015", true},
		"legacy 412":             {412, `{"status":"error","error_code":"70502","error_description":"Custom group name is already in use."}`, "70502", false},
		"legacy 404":             {404, `{"status":"error","error_code":"70506","error_description":"No such Custom Group found."}`, "70506", true},
		"dcapi 404":              {404, `{"statusCode":404,"errorCode":"30028","message":"Remote office not found"}`, "30028", true},
		"dcapi numeric code":     {400, `{"statusCode":"400","errorCode":30059,"message":"Local office cannot be deleted"}`, "30059", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := &Client{BaseURL: srv.URL, Tokens: StaticToken("t")}
			err := c.Do(context.Background(), Request{Method: http.MethodGet, Path: "/x"}, nil)
			ae, ok := err.(*APIError)
			if !ok {
				t.Fatalf("expected *APIError, got %T %v", err, err)
			}
			if ae.Code != tc.code {
				t.Errorf("code = %q, want %q", ae.Code, tc.code)
			}
			if IsNotFound(err) != tc.notFound {
				t.Errorf("IsNotFound = %v, want %v", IsNotFound(err), tc.notFound)
			}
		})
	}
}

func TestDo_retriesServerErrors(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Tokens: StaticToken("t")}
	if err := c.Do(context.Background(), Request{Method: http.MethodGet, Path: "/x"}, nil); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", n.Load())
	}
}

func TestCustomGroupInput_resourceIDsEncoding(t *testing.T) {
	b, _ := json.Marshal(CustomGroupInput{GroupName: "a", ResourceIDs: []int64{}})
	if string(b) != `{"groupName":"a","resourceIds":[]}` {
		t.Errorf("static empty members: %s", b)
	}
	b, _ = json.Marshal(CustomGroupInput{GroupName: "a"})
	if string(b) != `{"groupName":"a"}` {
		t.Errorf("dynamic (nil members): %s", b)
	}
}

func TestFlexString(t *testing.T) {
	var v struct{ A, B, C FlexString }
	if err := json.Unmarshal([]byte(`{"A":301,"B":"302","C":null}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.A != "301" || v.B != "302" || v.C != "" || v.A.Int64() != 301 {
		t.Fatalf("got %+v", v)
	}
}

// fakeGroupAPI serves one static group whose member list can only be replaced
// as a whole, with a delay on reads so unsynchronized read-modify-write
// cycles would overlap and lose updates.
func fakeGroupAPI(t *testing.T) (*httptest.Server, func() []int64) {
	var mu sync.Mutex
	members := []int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/1.4/customgroup/getCGList":
			_, _ = w.Write([]byte(`{"status":"success","message_response":{"getcglist":{"response":{"result":{"cg_list":[{"group_id":7,"group_name":"g","group_category":"Static","group_type":"Computers"}]}}}}}`))
		case "/api/1.4/customgroup/getCGDetails":
			mu.Lock()
			cur := slices.Clone(members)
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			list := make([]map[string]any, len(cur))
			for i, id := range cur {
				list[i] = map[string]any{"resourceId": strconv.FormatInt(id, 10)}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "message_response": map[string]any{"getcgdetails": map[string]any{"response": map[string]any{"result": map[string]any{
				"group_id": 7, "group_name": "g", "group_category": "Static", "group_type": "Computers", "groupMembers": list}}}}})
		case "/api/1.4/customgroup/updateCg":
			var b struct {
				ResourceIDs []int64 `json:"resourceIds"`
			}
			_ = json.NewDecoder(r.Body).Decode(&b)
			mu.Lock()
			members = b.ResourceIDs
			mu.Unlock()
			_, _ = w.Write([]byte(`{"status":"success","message_response":{"updatecg":{"cgResourceId":"7"}}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	return srv, func() []int64 { mu.Lock(); defer mu.Unlock(); return slices.Clone(members) }
}

func TestModifyCustomGroupMembers_concurrentAddsAreNotLost(t *testing.T) {
	srv, members := fakeGroupAPI(t)
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Tokens: StaticToken("t")}

	var wg sync.WaitGroup
	for i := int64(1); i <= 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := c.ModifyCustomGroupMembers(context.Background(), "7", func(cur []int64) ([]int64, bool) {
				return append(cur, i), true
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got := members()
	slices.Sort(got)
	if want := []int64{1, 2, 3, 4, 5, 6, 7, 8}; !slices.Equal(got, want) {
		t.Fatalf("members = %v, want %v (updates were lost)", got, want)
	}
}

// A 401 that refuses the request (a scope the call does not accept, or
// "Access denied", which is also what a deleted configuration answers) does
// not fetch a new token: that would not help and Zoho rate-limits tokens.
func TestDo_noNewTokenOnDenial(t *testing.T) {
	for _, body := range []string{`{"errorMsg":"INVALID_OAUTHSCOPE"}`,
		`{"errorCode":"1001","errorMsg":"Access denied - You are not allowed to access the requested resource."}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(body))
		}))
		tokens := &countingTokens{}
		c := &Client{BaseURL: srv.URL, Tokens: tokens}
		err := c.Do(context.Background(), Request{Method: http.MethodGet, Path: "/x"}, nil)
		srv.Close()
		var ae *APIError
		if !errors.As(err, &ae) || ae.HTTPStatus != http.StatusUnauthorized || ae.Message == "" {
			t.Fatalf("%s: err = %v, want a 401 APIError with its message", body, err)
		}
		if n := tokens.invalidated.Load(); n != 0 {
			t.Fatalf("%s: token invalidated %d times, want 0", body, n)
		}
	}
}

func TestGetConfiguration_deletedIsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"errorCode":"1001","errorMsg":"Access denied - You are not allowed to access the requested resource."}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Tokens: &countingTokens{}}
	if _, err := c.GetConfiguration(context.Background(), "52501000000418097"); !IsNotFound(err) {
		t.Fatalf("err = %v, want not found", err)
	}
}
