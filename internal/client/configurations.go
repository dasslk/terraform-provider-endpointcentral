package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Configurations and the Script Repository, through the calls Endpoint
// Central's console makes. Changing a configuration and every Script
// Repository write need DesktopCentralCloud.Common.CREATE, which the published
// spec does not mention; without it they answer INVALID_OAUTHSCOPE (creating a
// configuration works with the Configurations scopes alone).

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

// UpdateConfiguration changes a configuration with the console's modify call.
// The body is the create body plus collectionId and each script's detailId.
// Without "immediate", the change reaches computers at their next refresh.
func (c *Client) UpdateConfiguration(ctx context.Context, id string, body map[string]any) error {
	return c.Do(ctx, Request{
		Method:      http.MethodPost,
		Path:        "/dcapi/configuration/" + url.PathEscape(id) + "/deploy",
		Body:        body,
		ContentType: "application/deployConfig.v1+json",
		Accept:      "application/configDeployStatus.v1+json",
	}, nil)
}

// UploadScriptFile stores a script in the temporary file store, for
// CreateScript and UpdateScript.
func (c *Client) UploadScriptFile(ctx context.Context, fileName string, data []byte) (string, error) {
	return c.uploadFile(ctx, false, "Script_Repo", "application/octet-stream", fileName, data)
}

// ScriptInput is what the console sends to add or change a script.
type ScriptInput struct {
	Arguments   string `json:"arguments"`
	Description string `json:"description"`
	ExitCodes   string `json:"exitCodes"`
	Shared      bool   `json:"shareScript"`
	Platform    string `json:"platform"`
	ScriptFile  struct {
		FileID string `json:"fileID"`
	} `json:"scriptFile"`
}

// ScriptDetails is a script as GET /dcapi/configuration/scriptRepository/{id}
// returns it. The content is not returned.
type ScriptDetails struct {
	Description string `json:"description"`
	ExitCodes   string `json:"exitCodes"`
	Platform    string `json:"platform"`
	Arguments   string `json:"arguments"`
	Shared      bool   `json:"shareScript"`
	ScriptFile  struct {
		FileID   FlexString `json:"fileID"`
		FileName string     `json:"fileName"`
	} `json:"scriptFile"`
}

// CreateScript adds an uploaded script to the Script Repository and returns
// its ID.
func (c *Client) CreateScript(ctx context.Context, in ScriptInput) (string, error) {
	var out struct {
		ScriptID FlexString `json:"scriptId"`
	}
	err := c.Do(ctx, Request{
		Method:      http.MethodPost,
		Path:        "/dcapi/configuration/scriptRepository/script",
		Body:        in,
		ContentType: "application/scriptDetails.v1+json",
		Accept:      "application/addScriptStatus.v1+json",
	}, &out)
	if err != nil {
		return "", err
	}
	if out.ScriptID == "" {
		return "", errors.New("adding the script returned no script ID")
	}
	return out.ScriptID.String(), nil
}

// GetScript reads a script in the Script Repository.
func (c *Client) GetScript(ctx context.Context, id string) (*ScriptDetails, error) {
	var out ScriptDetails
	err := c.Do(ctx, Request{Method: http.MethodGet, Path: "/dcapi/configuration/scriptRepository/" + url.PathEscape(id), Accept: "*/*"}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateScript changes a script; the file is uploaded again each time.
func (c *Client) UpdateScript(ctx context.Context, id string, in ScriptInput) error {
	return c.Do(ctx, Request{
		Method:      http.MethodPost,
		Path:        "/dcapi/configuration/scriptRepository/" + url.PathEscape(id),
		Body:        in,
		ContentType: "application/scriptDetails.v1+json",
		Accept:      "*/*",
	}, nil)
}

// DeleteScript removes a script from the Script Repository. Endpoint Central
// refuses while the script is in use; the error says where.
func (c *Client) DeleteScript(ctx context.Context, id string) error {
	var out struct {
		Deleted          bool `json:"scriptDeleted"`
		InConfig         bool `json:"isUsedInConfig"`
		InPolicy         bool `json:"isUsedInDeploymentPolicy"`
		InDynamicGroup   bool `json:"isUsedInDynamicCustomGroup"`
		InEndpointAction bool `json:"isUsedInEA"`
	}
	if err := c.Do(ctx, Request{Method: http.MethodDelete, Path: "/dcapi/configuration/scriptRepository/" + url.PathEscape(id), Accept: "*/*"}, &out); err != nil {
		return err
	}
	if out.Deleted {
		return nil
	}
	var uses []string
	for _, u := range []struct {
		used bool
		what string
	}{{out.InConfig, "a configuration"}, {out.InPolicy, "a deployment policy"},
		{out.InDynamicGroup, "a dynamic custom group"}, {out.InEndpointAction, "an endpoint action"}} {
		if u.used {
			uses = append(uses, u.what)
		}
	}
	if len(uses) == 0 {
		return fmt.Errorf("script %s was not deleted", id)
	}
	return fmt.Errorf("script %s was not deleted: it is used in %s", id, strings.Join(uses, ", "))
}
