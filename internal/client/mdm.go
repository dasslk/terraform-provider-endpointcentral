package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
)

// The MDM API (https://www.manageengine.com/mobile-device-management/api/),
// served on mdm.manageengine.<region>. Endpoint Central's own calls for Mac
// custom profiles only accept a browser session, so Mac profiles are managed
// here. Behaviour verified on a live tenant (2026-11-11) where it differs from
// the documentation:
//
//   - macOS is platform_type 6 (the docs list 1-4); maccustomprofiles is
//     rejected on type 1.
//   - Uploads go to /emsapi/files on the MDM host; it answers 406 to
//     "Accept: application/json".
//   - Deleting needs DELETE /profiles/trash first (from the console); a plain
//     DELETE /profiles answers 412 until then. A trashed profile answers GET
//     with HTTP 500.
//   - Editing a payload item gives it a new ID.
//   - GET /groups/{id}/profiles lists a group's profiles (undocumented).

const (
	// MDMPlatformMac is the platform_type of macOS profiles.
	MDMPlatformMac = 6
	// MDMGroupTypeDevice is the group_type of device groups.
	MDMGroupTypeDevice = 6

	macCustomProfilesPayload = "maccustomprofiles"
)

// MDMProfile is a profile in the MDM API.
type MDMProfile struct {
	ProfileID      FlexString `json:"profile_id"`
	Name           string     `json:"profile_name"`
	Description    string     `json:"profile_description"`
	PlatformType   FlexString `json:"platform_type"`
	Status         string     `json:"profile_status"`
	Version        FlexString `json:"profile_version"`
	MovedToTrash   FlexString `json:"is_moved_to_trash"`
	Trashed        FlexString `json:"trashed"`
	Payloads       []string   `json:"payloads"`
	AssociatedGrps FlexString `json:"profile_associated_groups_count"`
	AssociatedDevs FlexString `json:"profile_associated_devices_count"`
}

// IsTrashed reports whether the profile sits in the trash.
func (p *MDMProfile) IsTrashed() bool {
	return p.MovedToTrash.String() == "true" || p.Trashed.String() == "1"
}

func jsonBody(v any) Request { return Request{Body: v, ContentType: "application/json", MDM: true} }

// UploadMacProfileFile uploads a .mobileconfig and returns its file ID. Files
// expire a few hours after the upload, so upload right before using one.
func (c *Client) UploadMacProfileFile(ctx context.Context, fileName string, data []byte) (string, error) {
	return c.uploadFile(ctx, true, "MDM_CUSTOM_PROFILE", "application/x-apple-aspen-config", fileName, data)
}

// uploadFile stores a file in the temporary file store of the Endpoint
// Central host (mdm false) or the MDM host and returns its file ID. module
// names what the file is for.
func (c *Client) uploadFile(ctx context.Context, mdm bool, module, contentType, fileName string, data []byte) (string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, fileName))
	h.Set("Content-Type", contentType)
	part, err := mw.CreatePart(h)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}
	var raw json.RawMessage
	err = c.Do(ctx, Request{
		Method:      http.MethodPost,
		Path:        "/emsapi/files",
		RawBody:     buf.Bytes(),
		ContentType: mw.FormDataContentType(),
		Accept:      "*/*",
		Headers:     map[string]string{"Module": module},
		MDM:         mdm,
	}, &raw)
	if err != nil {
		return "", fmt.Errorf("uploading %s: %w", fileName, err)
	}
	type uploaded struct {
		FileID FlexString `json:"fileID"`
	}
	var files []uploaded
	if json.Unmarshal(raw, &files) != nil {
		var one uploaded
		if err := json.Unmarshal(raw, &one); err != nil {
			return "", fmt.Errorf("decoding upload response: %w", err)
		}
		files = []uploaded{one}
	}
	if len(files) == 0 || files[0].FileID == "" {
		return "", fmt.Errorf("uploading %s returned no file ID", fileName)
	}
	return files[0].FileID.String(), nil
}

// CreateMacProfile creates an empty, unpublished macOS profile.
func (c *Client) CreateMacProfile(ctx context.Context, name, description string) (*MDMProfile, error) {
	r := jsonBody(map[string]any{"profile_name": name, "profile_description": description, "platform_type": MDMPlatformMac})
	r.Method, r.Path = http.MethodPost, "/api/v1/mdm/profiles"
	var out MDMProfile
	if err := c.Do(ctx, r, &out); err != nil {
		return nil, err
	}
	if out.ProfileID == "" {
		return nil, errors.New("creating the profile returned no profile ID")
	}
	return &out, nil
}

// GetMDMProfile reads a profile. A trashed or deleted profile answers HTTP
// 500, so that case is checked against the profile list and reported as not
// found.
func (c *Client) GetMDMProfile(ctx context.Context, id string) (*MDMProfile, error) {
	var out MDMProfile
	err := c.Do(ctx, Request{Method: http.MethodGet, Path: "/api/v1/mdm/profiles/" + url.PathEscape(id), MDM: true, NoServerErrorRetry: true}, &out)
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.HTTPStatus >= 500 {
			if p, lerr := c.findMDMProfile(ctx, id); lerr == nil {
				return p, nil
			} else if IsNotFound(lerr) {
				return nil, lerr
			}
		}
		return nil, err
	}
	if out.IsTrashed() {
		return nil, &APIError{HTTPStatus: http.StatusNotFound, Message: fmt.Sprintf("profile %s is in the trash", id)}
	}
	return &out, nil
}

// findMDMProfile looks a profile up in the profile list, following its paging.
func (c *Client) findMDMProfile(ctx context.Context, id string) (*MDMProfile, error) {
	path := "/api/v1/mdm/profiles"
	var q url.Values
	for {
		var out struct {
			Profiles []MDMProfile `json:"profiles"`
			Paging   struct {
				Next string `json:"next"`
			} `json:"paging"`
		}
		if err := c.Do(ctx, Request{Method: http.MethodGet, Path: path, Query: q, MDM: true}, &out); err != nil {
			return nil, err
		}
		for i := range out.Profiles {
			if out.Profiles[i].ProfileID.String() == id && !out.Profiles[i].IsTrashed() {
				return &out.Profiles[i], nil
			}
		}
		next, err := url.Parse(out.Paging.Next)
		if out.Paging.Next == "" || err != nil {
			return nil, &APIError{HTTPStatus: http.StatusNotFound, Message: fmt.Sprintf("profile %s not found", id)}
		}
		path, q = next.Path, next.Query()
	}
}

// UpdateMDMProfile changes a profile's name and description.
func (c *Client) UpdateMDMProfile(ctx context.Context, id, name, description string) error {
	r := jsonBody(map[string]any{"profile_name": name, "profile_description": description})
	r.Method, r.Path = http.MethodPut, "/api/v1/mdm/profiles/"+url.PathEscape(id)
	return c.Do(ctx, r, nil)
}

// SetMacProfileContent makes the uploaded file the profile's custom profile
// payload, adding the payload or replacing the existing one.
func (c *Client) SetMacProfileContent(ctx context.Context, id, fileID string) error {
	base := "/api/v1/mdm/profiles/" + url.PathEscape(id) + "/payloads/" + macCustomProfilesPayload
	var items struct {
		PayloadItems []FlexString `json:"payloaditems"`
	}
	err := c.Do(ctx, Request{Method: http.MethodGet, Path: base, MDM: true}, &items)
	if err != nil && !IsNotFound(err) {
		return err
	}
	fid, perr := parseID(fileID)
	if perr != nil {
		return perr
	}
	// allow_existing_payload, as the MDM console sends it, accepts payload
	// types the MDM also has its own forms for (firewall, login window,
	// screen saver, restrictions, VPN, ...). Without it the API refuses such
	// a profile with PAY0011 "Payloads supported by MDM cannot be added".
	r := jsonBody(map[string]any{
		"custom_profile_file_id": fid,
		"custom_profile_type":    1,
		"allow_existing_payload": true,
	})
	if len(items.PayloadItems) == 0 {
		r.Method, r.Path = http.MethodPost, base
	} else {
		// The item's ID changes with every edit, so it is looked up each time.
		r.Method, r.Path = http.MethodPut, base+"/payloaditems/"+url.PathEscape(items.PayloadItems[0].String())
	}
	return c.Do(ctx, r, nil)
}

// PublishMDMProfile publishes the profile's current version.
func (c *Client) PublishMDMProfile(ctx context.Context, id string) error {
	return c.Do(ctx, Request{Method: http.MethodPost, Path: "/api/v1/mdm/profiles/" + url.PathEscape(id) + "/publish", MDM: true}, nil)
}

// UpdateMDMProfileEverywhere pushes the published version to every group and
// device the profile is assigned to.
func (c *Client) UpdateMDMProfileEverywhere(ctx context.Context, id string) error {
	return c.Do(ctx, Request{Method: http.MethodPut, Path: "/api/v1/mdm/profiles/" + url.PathEscape(id) + "/update_all", MDM: true}, nil)
}

// DeleteMDMProfile moves the profile to the trash and deletes it permanently.
func (c *Client) DeleteMDMProfile(ctx context.Context, id string) error {
	n, err := parseID(id)
	if err != nil {
		return err
	}
	body := map[string][]int64{"profile_ids": {n}}
	r := jsonBody(body)
	r.Method, r.Path = http.MethodDelete, "/api/v1/mdm/profiles/trash"
	if err := c.Do(ctx, r, nil); err != nil {
		return fmt.Errorf("moving profile %s to the trash: %w", id, err)
	}
	r = jsonBody(body)
	r.Method, r.Path = http.MethodDelete, "/api/v1/mdm/profiles"
	return c.Do(ctx, r, nil)
}

// MDMGroup is a group in the MDM API.
type MDMGroup struct {
	GroupID     FlexString `json:"group_id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	GroupType   FlexString `json:"group_type"`
	Domain      string     `json:"domain"`
}

func (c *Client) CreateMDMGroup(ctx context.Context, name, description string) (*MDMGroup, error) {
	body := map[string]any{"name": name, "group_type": MDMGroupTypeDevice}
	if description != "" {
		body["description"] = description
	}
	r := jsonBody(body)
	r.Method, r.Path = http.MethodPost, "/api/v1/mdm/groups"
	var out MDMGroup
	if err := c.Do(ctx, r, &out); err != nil {
		return nil, err
	}
	if out.GroupID == "" {
		return nil, errors.New("creating the group returned no group ID")
	}
	return &out, nil
}

func (c *Client) GetMDMGroup(ctx context.Context, id string) (*MDMGroup, error) {
	var out MDMGroup
	if err := c.Do(ctx, Request{Method: http.MethodGet, Path: "/api/v1/mdm/groups/" + url.PathEscape(id), MDM: true}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListMDMGroups lists every group.
func (c *Client) ListMDMGroups(ctx context.Context) ([]MDMGroup, error) {
	var out struct {
		Groups []MDMGroup `json:"groups"`
	}
	if err := c.Do(ctx, Request{Method: http.MethodGet, Path: "/api/v1/mdm/groups", MDM: true}, &out); err != nil {
		return nil, err
	}
	return out.Groups, nil
}

func (c *Client) DeleteMDMGroup(ctx context.Context, id string) error {
	return c.Do(ctx, Request{Method: http.MethodDelete, Path: "/api/v1/mdm/groups/" + url.PathEscape(id), MDM: true}, nil)
}

// MDMGroupMembers returns the device IDs in a group.
func (c *Client) MDMGroupMembers(ctx context.Context, groupID string) ([]string, error) {
	var out struct {
		MemberIDs []FlexString `json:"member_ids"`
	}
	if err := c.Do(ctx, Request{Method: http.MethodGet, Path: "/api/v1/mdm/groups/" + url.PathEscape(groupID) + "/members", MDM: true}, &out); err != nil {
		return nil, err
	}
	return flexStrings(out.MemberIDs), nil
}

func (c *Client) AddMDMGroupMember(ctx context.Context, groupID, deviceID string) error {
	return c.Do(ctx, Request{Method: http.MethodPost, Path: "/api/v1/mdm/groups/" + url.PathEscape(groupID) + "/members/" + url.PathEscape(deviceID), MDM: true}, nil)
}

func (c *Client) RemoveMDMGroupMember(ctx context.Context, groupID, deviceID string) error {
	return c.Do(ctx, Request{Method: http.MethodDelete, Path: "/api/v1/mdm/groups/" + url.PathEscape(groupID) + "/members/" + url.PathEscape(deviceID), MDM: true}, nil)
}

// MDMGroupProfiles returns the IDs of the profiles assigned to a group.
func (c *Client) MDMGroupProfiles(ctx context.Context, groupID string) ([]string, error) {
	var out struct {
		Profiles []FlexString `json:"profiles"`
	}
	if err := c.Do(ctx, Request{Method: http.MethodGet, Path: "/api/v1/mdm/groups/" + url.PathEscape(groupID) + "/profiles", MDM: true}, &out); err != nil {
		return nil, err
	}
	return flexStrings(out.Profiles), nil
}

func (c *Client) setMDMGroupProfile(ctx context.Context, method, groupID, profileID string) error {
	n, err := parseID(profileID)
	if err != nil {
		return err
	}
	r := jsonBody(map[string][]int64{"profile_ids": {n}})
	r.Method, r.Path = method, "/api/v1/mdm/groups/"+url.PathEscape(groupID)+"/profiles"
	return c.Do(ctx, r, nil)
}

// AssignMDMProfile assigns a profile to a group; its devices install it.
func (c *Client) AssignMDMProfile(ctx context.Context, groupID, profileID string) error {
	return c.setMDMGroupProfile(ctx, http.MethodPost, groupID, profileID)
}

// UnassignMDMProfile removes a profile from a group; its devices remove it.
func (c *Client) UnassignMDMProfile(ctx context.Context, groupID, profileID string) error {
	return c.setMDMGroupProfile(ctx, http.MethodDelete, groupID, profileID)
}

// MDMDevice is a device in the MDM API.
type MDMDevice struct {
	DeviceID     FlexString `json:"device_id"`
	Name         string     `json:"device_name"`
	SerialNumber string     `json:"serial_number"`
	Model        string     `json:"model"`
	UDID         string     `json:"udid"`
	PlatformType string     `json:"platform_type"`
	// DeviceType is "3" for Macs on a live tenant; Apple phones, tablets and
	// Macs all report platform_type "ios".
	DeviceType    FlexString `json:"device_type"`
	OSVersion     string     `json:"os_version"`
	ManagedStatus FlexString `json:"managed_status"`
	IsRemoved     FlexString `json:"is_removed"`
}

// Removed reports whether the device was removed from management.
func (d MDMDevice) Removed() bool { return d.IsRemoved.String() == "true" }

// IsVirtualMac reports whether the device is a macOS virtual machine: Apple
// gives them model identifiers starting with "VirtualMac".
func (d MDMDevice) IsVirtualMac() bool { return strings.HasPrefix(d.Model, "VirtualMac") }

// ListMDMDevices lists every device, following the API's paging.
func (c *Client) ListMDMDevices(ctx context.Context) ([]MDMDevice, error) {
	path := "/api/v1/mdm/devices"
	var q url.Values
	var all []MDMDevice
	for {
		var out struct {
			Devices []MDMDevice `json:"devices"`
			Paging  struct {
				Next string `json:"next"`
			} `json:"paging"`
		}
		if err := c.Do(ctx, Request{Method: http.MethodGet, Path: path, Query: q, MDM: true}, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Devices...)
		if out.Paging.Next == "" {
			return all, nil
		}
		next, err := url.Parse(out.Paging.Next)
		if err != nil {
			return nil, fmt.Errorf("listing MDM devices: bad next page link %q: %w", out.Paging.Next, err)
		}
		path, q = next.Path, next.Query()
	}
}

func flexStrings(in []FlexString) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = v.String()
	}
	return out
}

func parseID(s string) (int64, error) {
	var n int64
	if _, err := fmt.Sscan(s, &n); err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid ID %q", s)
	}
	return n, nil
}
