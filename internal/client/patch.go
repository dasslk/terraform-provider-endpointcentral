package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// The /dcapi patch endpoints answer 406 to "Accept: application/json" on a
// live tenant (2026-11-11) and declare no media type of their own.
const patchAccept = "*/*"

// Automated Patch Deployment (APD) tasks, through the calls Endpoint
// Central's console makes, verified on a live tenant (2026-11-11). The 1.4
// createAPDTask/modifyAPDTask calls are not usable on Cloud (create refuses
// OAuth, modify redirects to URL_RULE_NOT_CONFIGURED).
//
//	create  POST   /dcapi/patch/autopatchdeployment             application/addTask.v1+json
//	read    GET    /dcapi/patch/autopatchdeployment/{task_id}   Accept */*
//	update  PUT    /dcapi/patch/autopatchdeployment/{task_id}   application/addModifyTask.v1+json
//	suspend POST   /dcapi/patch/autopatchdeployment/suspend     application/suspend.v1+json, ["<task_id>"]
//	resume  POST   /dcapi/patch/autopatchdeployment/resume      application/resume.v1+json, ["<task_id>"]
//	delete  DELETE /dcapi/patch/autopatchdeployment/delete      application/deletetask.v1+json, ["<task_id>"]
//
// An unknown task ID answers HTTP 500 {"errorCode":"1003","errorMsg":"Invalid TaskID"}.

const apdPath = "/dcapi/patch/autopatchdeployment"

// Target type codes in an APD task's targetData.
const (
	// TargetCodeWorkgroup is the main target: the workgroup (or domain) the
	// task's computers belong to.
	TargetCodeWorkgroup = "4"
	// TargetCodeCustomGroupRefine is a custom group narrowing the main
	// target, as an include or an exclude.
	TargetCodeCustomGroupRefine = "15"
)

// APDWritableFields are the fields the console sends when it saves a task.
// A read returns more (status, timestamps, read-only target flags), which the
// update call rejects ("Invalid parameter ... detected").
var APDWritableFields = []string{
	"deploymentType", "platformId", "label", "description", "osUpdates", "tpSeverities",
	"includeOSAppType", "includeTPAppType", "osApplications", "tpApplications",
	"definitionUpdates", "driver", "bios", "delayDeploymentType", "delayDuration", "templateId",
	"taskExpiryEnabled", "expiryDate", "reportNotificationSettings", "failureNotificationSettings",
	"email", "usersToBeNotified", "enablePushNotification", "targetData",
}

// APDTaskDefaults is the body of a new task before the managed fields are
// set: what the console sends for options left at their defaults.
func APDTaskDefaults() map[string]any {
	return map[string]any{
		"deploymentType": 0, "description": "", "osUpdates": []any{}, "tpSeverities": []any{},
		"includeOSAppType": 0, "includeTPAppType": 0, "osApplications": []any{}, "tpApplications": []any{},
		"definitionUpdates": false, "driver": false, "bios": false,
		"delayDeploymentType": 0, "delayDuration": 0, "taskExpiryEnabled": false,
		"reportNotificationSettings":  map[string]any{"enableNotify": false, "reportDuration": 1, "enableAttachment": false, "attachmentFormat": 1},
		"failureNotificationSettings": map[string]any{"enableNotify": false, "reportDuration": 3, "enableAttachment": false, "attachmentFormat": 1},
		"email":                       []any{""}, "usersToBeNotified": []any{}, "enablePushNotification": false,
	}
}

// IsAPDTaskNotFound recognises the API's answer for an unknown task ID.
func IsAPDTaskNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && (ae.Code == "1003" || strings.Contains(ae.Message, "Invalid TaskID"))
}

// GetAPDTask returns a task's full settings as the API reports them.
func (c *Client) GetAPDTask(ctx context.Context, taskID string) (map[string]any, error) {
	var out map[string]any
	err := c.Do(ctx, Request{Method: http.MethodGet, Path: apdPath + "/" + url.PathEscape(taskID), Accept: patchAccept, NoServerErrorRetry: true}, &out)
	if err != nil {
		if IsAPDTaskNotFound(err) {
			return nil, &APIError{HTTPStatus: http.StatusNotFound, Message: fmt.Sprintf("APD task %s not found", taskID)}
		}
		return nil, err
	}
	return out, nil
}

// WritableAPDBody keeps the fields an update accepts, in the shape it
// accepts: osUpdates without their names, targets without read-only flags.
func WritableAPDBody(task map[string]any) map[string]any {
	body := map[string]any{}
	for _, k := range APDWritableFields {
		if v, ok := task[k]; ok {
			body[k] = v
		}
	}
	if ups, ok := body["osUpdates"].([]any); ok {
		clean := make([]any, 0, len(ups))
		for _, u := range ups {
			if m, ok := u.(map[string]any); ok {
				clean = append(clean, map[string]any{"id": m["id"], "severities": m["severities"]})
			}
		}
		body["osUpdates"] = clean
	}
	if e, ok := body["email"].([]any); ok && len(e) == 0 {
		body["email"] = []any{""} // what the console sends for "no e-mail"
	}
	if td, ok := body["targetData"].(map[string]any); ok {
		body["targetData"] = map[string]any{"allTargets": cleanTargets(td["allTargets"])}
	}
	return body
}

func cleanSelection(v any) map[string]any {
	m, _ := v.(map[string]any)
	vals, _ := m["selectedValues"].([]any)
	out := make([]any, 0, len(vals))
	for _, sv := range vals {
		s, _ := sv.(map[string]any)
		out = append(out, map[string]any{"idValue": s["idValue"], "label": s["label"]})
	}
	return map[string]any{"targetType": m["targetType"], "selectedValues": out}
}

func cleanTargets(v any) []any {
	all, _ := v.([]any)
	out := make([]any, 0, len(all))
	for _, t := range all {
		m, _ := t.(map[string]any)
		e := map[string]any{"mainTarget": cleanSelection(m["mainTarget"])}
		if r, ok := m["computerRefineTarget"].(map[string]any); ok {
			cr := map[string]any{}
			for _, k := range []string{"includeTargets", "excludeTargets"} {
				if list, ok := r[k].([]any); ok && len(list) > 0 {
					sels := make([]any, len(list))
					for i, s := range list {
						sels[i] = cleanSelection(s)
					}
					cr[k] = sels
				}
			}
			if len(cr) > 0 {
				e["computerRefineTarget"] = cr
			}
		}
		out = append(out, e)
	}
	return out
}

// CreateAPDTask creates a task and returns its task ID. The create answer
// only carries the configuration (collection) ID, so the task ID is looked up
// in the task report.
func (c *Client) CreateAPDTask(ctx context.Context, body map[string]any) (string, error) {
	var out struct {
		CollectionID FlexString `json:"collectionId"`
		TaskMsg      string     `json:"taskMsg"`
	}
	err := c.Do(ctx, Request{Method: http.MethodPost, Path: apdPath, Body: body,
		ContentType: "application/addTask.v1+json", Accept: patchAccept}, &out)
	if err != nil {
		return "", err
	}
	tasks, err := c.ListAPDTasks(ctx)
	if err != nil {
		return "", fmt.Errorf("APD task created (%s) but listing tasks to find its ID failed: %w", out.TaskMsg, err)
	}
	for _, t := range tasks {
		if t.CollectionID == out.CollectionID && out.CollectionID != "" {
			return t.TaskID.String(), nil
		}
	}
	return "", fmt.Errorf("APD task created (%s) but not found in the task report", out.TaskMsg)
}

// UpdateAPDTask replaces a task's settings.
func (c *Client) UpdateAPDTask(ctx context.Context, taskID string, body map[string]any) error {
	return c.Do(ctx, Request{Method: http.MethodPut, Path: apdPath + "/" + url.PathEscape(taskID), Body: body,
		ContentType: "application/addModifyTask.v1+json", Accept: patchAccept}, nil)
}

// apdAction runs suspend, resume or delete on one task. The API answers with
// {"<task_id>": true|false}.
func (c *Client) apdAction(ctx context.Context, method, action, mediaType, taskID string) error {
	var out map[string]bool
	err := c.Do(ctx, Request{Method: method, Path: apdPath + "/" + action, Body: []string{taskID},
		ContentType: mediaType, Accept: patchAccept}, &out)
	if err != nil {
		return err
	}
	if ok, found := out[taskID]; found && !ok {
		return fmt.Errorf("%s of APD task %s was refused", action, taskID)
	}
	return nil
}

func (c *Client) SuspendAPDTask(ctx context.Context, taskID string) error {
	return c.apdAction(ctx, http.MethodPost, "suspend", "application/suspend.v1+json", taskID)
}

func (c *Client) ResumeAPDTask(ctx context.Context, taskID string) error {
	return c.apdAction(ctx, http.MethodPost, "resume", "application/resume.v1+json", taskID)
}

func (c *Client) DeleteAPDTask(ctx context.Context, taskID string) error {
	return c.apdAction(ctx, http.MethodDelete, "delete", "application/deletetask.v1+json", taskID)
}

// APDTask is one row of the APD task report.
type APDTask struct {
	TaskID           FlexString `json:"task_id"`
	TaskName         string     `json:"task_name"`
	OSPlatformName   string     `json:"os_platform_name"`
	OSPlatformID     FlexString `json:"os_platform_id"`
	CollectionID     FlexString `json:"collection_id"`
	CollectionStatus FlexString `json:"collection_status"`
	TemplateID       FlexString `json:"template_id"`
	TotalTarget      FlexString `json:"total_target"`
}

// ListAPDTasks returns every APD task from the APD task report.
func (c *Client) ListAPDTasks(ctx context.Context) ([]APDTask, error) {
	const limit = 100
	var all []APDTask
	for page := 1; ; page++ {
		var out struct {
			TotalRecords FlexString `json:"totalRecords"`
			Rows         []APDTask  `json:"messageResponse"`
		}
		err := c.Do(ctx, Request{
			Method: http.MethodGet,
			Path:   "/dcapi/patch/reports/apdTasks",
			Query:  url.Values{"page": {strconv.Itoa(page)}, "pageLimit": {strconv.Itoa(limit)}},
			Accept: patchAccept,
		}, &out)
		if err != nil {
			return nil, err
		}
		all = append(all, out.Rows...)
		if lastPage(len(out.Rows), len(all), int(out.TotalRecords.Int64()), limit) {
			return all, nil
		}
	}
}

// FindAPDTaskByName finds a task in the task report by its exact name.
func (c *Client) FindAPDTaskByName(ctx context.Context, name string) (*APDTask, error) {
	tasks, err := c.ListAPDTasks(ctx)
	if err != nil {
		return nil, err
	}
	for i := range tasks {
		if tasks[i].TaskName == name {
			return &tasks[i], nil
		}
	}
	return nil, &APIError{HTTPStatus: http.StatusNotFound, Message: fmt.Sprintf("APD task %q not found", name)}
}

// DeploymentPolicy is a patch deployment policy (called a template in the API).
//
// The spec documents {"deploymentPolicies": [{templateID, templateName,
// platformId, ...}]}; a live tenant answers {"createdByOthers": [...],
// "selfCreated": [...], "systemCreated": [<ids>]} with entries {tempID,
// tempName, platformScope}. Both are read.
type DeploymentPolicy struct {
	TemplateID    FlexString `json:"templateID"`
	TemplateName  string     `json:"templateName"`
	TemplateDesc  string     `json:"templateDesc"`
	TemplateInUse bool       `json:"templateInUse"`
	PlatformID    FlexString `json:"platformId"`

	TempID        FlexString `json:"tempID"`
	TempName      string     `json:"tempName"`
	PlatformScope FlexString `json:"platformScope"`

	// SystemCreated is set for the policies ManageEngine ships.
	SystemCreated bool `json:"-"`
}

func (p *DeploymentPolicy) normalize() {
	if p.TemplateID == "" {
		p.TemplateID = p.TempID
	}
	if p.TemplateName == "" {
		p.TemplateName = p.TempName
	}
	if p.PlatformID == "" {
		p.PlatformID = p.PlatformScope
	}
}

func (c *Client) ListDeploymentPolicies(ctx context.Context) ([]DeploymentPolicy, error) {
	var out struct {
		DeploymentPolicies []DeploymentPolicy `json:"deploymentPolicies"`
		CreatedByOthers    []DeploymentPolicy `json:"createdByOthers"`
		SelfCreated        []DeploymentPolicy `json:"selfCreated"`
		SystemCreated      []FlexString       `json:"systemCreated"`
	}
	if err := c.Do(ctx, Request{Method: http.MethodGet, Path: "/dcapi/patch/deploymentPolicy", Accept: patchAccept}, &out); err != nil {
		return nil, err
	}
	all := append(append(out.DeploymentPolicies, out.CreatedByOthers...), out.SelfCreated...)
	system := flexStrings(out.SystemCreated)
	for i := range all {
		all[i].normalize()
		all[i].SystemCreated = slices.Contains(system, all[i].TemplateID.String())
	}
	return all, nil
}

// PlatformName maps the numeric platform ID used across the API to its name.
func PlatformName(id string) string {
	switch strings.TrimSpace(id) {
	case "1":
		return "windows"
	case "2":
		return "mac"
	case "3":
		return "linux"
	}
	return ""
}

// EqualJSON reports whether two values encode to the same JSON.
func EqualJSON(a, b any) bool {
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(x) == string(y)
}
