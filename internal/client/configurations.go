package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// Configurations through the calls Endpoint Central's console makes. With an
// OAuth token, configurations can be created, read and deleted, but not
// modified: the console's modify call (POST /dcapi/configuration/{id}/deploy)
// answers INVALID_OAUTHSCOPE whatever the scopes. The Script Repository can be
// listed, not written (upload and create answer INVALID_OAUTHSCOPE too).

// Script is a script in the Script Repository. Its default arguments are not
// decoded: they may hold secrets.
type Script struct {
	ID          FlexString `json:"scriptId"`
	Name        string     `json:"scriptName"`
	PlatformID  FlexString `json:"platformID"`
	ExitCodes   string     `json:"exitCodes"`
	Description string     `json:"description"`
}

// ListScripts lists the Script Repository's scripts for a platform: "mac",
// "linux" or "windows".
func (c *Client) ListScripts(ctx context.Context, platform string) ([]Script, error) {
	var out []Script
	err := c.Do(ctx, Request{
		Method: http.MethodGet,
		Path:   "/dcapi/configuration/scripts",
		Query:  url.Values{"platform": {platform}},
		Accept: "*/*",
	}, &out)
	return out, err
}

// MacCustomScriptConfigName is the configuration type of Mac custom scripts.
const MacCustomScriptConfigName = "CUSTOMSCRIPT_OSX"

// CreateMacCustomScript creates a Mac custom script configuration from the
// console's body and returns its collection ID.
func (c *Client) CreateMacCustomScript(ctx context.Context, body map[string]any) (string, error) {
	var out struct {
		CollectionID FlexString `json:"collectionId"`
	}
	err := c.Do(ctx, Request{
		Method:      http.MethodPost,
		Path:        "/dcapi/configuration/mac/customScript/deploy",
		Body:        body,
		ContentType: "application/deployMacCustomScript.v1+json",
		Accept:      "application/configDeployStatus.v1+json",
	}, &out)
	if err != nil {
		return "", err
	}
	if out.CollectionID == "" {
		return "", errors.New("creating the configuration returned no collection ID")
	}
	return out.CollectionID.String(), nil
}

// GetConfiguration reads a configuration as the console does. A deleted
// configuration answers 401 "Access denied" (errorCode 1001), which is
// returned as a not-found error.
func (c *Client) GetConfiguration(ctx context.Context, id string) (map[string]any, error) {
	var out map[string]any
	err := c.Do(ctx, Request{
		Method: http.MethodGet,
		Path:   "/dcapi/configuration/" + url.PathEscape(id),
		Accept: "application/collectionDetails.v1+json",
	}, &out)
	var ae *APIError
	if errors.As(err, &ae) && ae.HTTPStatus == http.StatusUnauthorized && ae.Code == "1001" {
		return nil, &APIError{HTTPStatus: http.StatusNotFound, Code: ae.Code, Message: fmt.Sprintf("configuration %s not found", id)}
	}
	return out, err
}

// DeleteConfiguration moves a configuration to the trash and deletes it from
// there.
func (c *Client) DeleteConfiguration(ctx context.Context, id string) error {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid configuration ID %q", id)
	}
	body := map[string][]string{"collectionIds": {strconv.FormatInt(n, 10)}}
	if err := c.Do(ctx, Request{
		Method:      http.MethodPost,
		Path:        "/dcapi/configuration/moveToTrash",
		Body:        body,
		ContentType: "application/moveConfigToTrash.v1+json",
		Accept:      "application/moveConfigToTrashStatus.v1+json",
	}, nil); err != nil {
		return err
	}
	return c.Do(ctx, Request{
		Method:      http.MethodDelete,
		Path:        "/dcapi/configuration/trash/delete",
		Body:        body,
		ContentType: "application/deleteConfigTrash.v1+json",
		Accept:      "application/deletedConfigStatus.v1+json",
	}, nil)
}
