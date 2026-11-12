package provider

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// fakeMDM imitates the MDM API on its own host, including the behaviour seen
// on a live tenant where it differs from the documentation (see client/mdm.go).
type fakeMDM struct {
	f      *fakeEC // shares the token check and the lock
	server *httptest.Server
	mu     *sync.Mutex
	nextID int64

	profiles   map[int64]*fakeMDMProfile
	groups     map[int64]*fakeMDMGroup
	devices    []fakeMDMDevice
	files      map[int64]string
	updateAlls int
}

type fakeMDMProfile struct {
	ID          int64
	Name        string
	Description string
	Platform    int
	PayloadItem int64 // 0 when there is no payload
	Content     string
	Published   string // content of the last published version
	Version     int
	Trashed     bool
}

type fakeMDMGroup struct {
	ID          int64
	Name        string
	Description string
	Members     []int64
	Profiles    []int64
}

type fakeMDMDevice struct {
	ID      int64
	Name    string
	Serial  string
	Model   string
	Removed bool
}

func newFakeMDM(f *fakeEC) *fakeMDM {
	m := &fakeMDM{
		f: f, mu: &f.mu, nextID: 200000000140000,
		profiles: map[int64]*fakeMDMProfile{},
		groups:   map[int64]*fakeMDMGroup{},
		files:    map[int64]string{},
		devices: []fakeMDMDevice{
			{ID: 200000000112017, Name: "mac-lab-02", Serial: "VMSERIAL002", Model: "VirtualMac2,1"},
			{ID: 200000000112018, Name: "mac-001", Serial: "C02XYZ123", Model: "Mac14,13"},
			{ID: 200000000110155, Name: "mac-lab-01", Serial: "VMSERIAL001", Model: "VirtualMac2,1", Removed: true},
			{ID: 200000000112019, Name: "MacBookPro", Serial: "MBPSERIAL01", Model: "MacBookPro18,3"},
		},
	}
	mux := http.NewServeMux()
	h := func(pattern string, fn http.HandlerFunc) { mux.HandleFunc(pattern, f.auth(fn)) }
	h("POST /emsapi/files", m.upload)
	h("POST /api/v1/mdm/profiles", m.createProfile)
	h("GET /api/v1/mdm/profiles", m.listProfiles)
	h("GET /api/v1/mdm/profiles/{id}", m.getProfile)
	h("PUT /api/v1/mdm/profiles/{id}", m.updateProfile)
	h("GET /api/v1/mdm/profiles/{id}/payloads/maccustomprofiles", m.getPayload)
	h("POST /api/v1/mdm/profiles/{id}/payloads/maccustomprofiles", m.setPayload(false))
	h("PUT /api/v1/mdm/profiles/{id}/payloads/maccustomprofiles/payloaditems/{item}", m.setPayload(true))
	h("POST /api/v1/mdm/profiles/{id}/publish", m.publish)
	h("PUT /api/v1/mdm/profiles/{id}/update_all", m.updateAll)
	h("DELETE /api/v1/mdm/profiles/trash", m.trash)
	h("DELETE /api/v1/mdm/profiles", m.deleteProfiles)
	h("POST /api/v1/mdm/groups", m.createGroup)
	h("GET /api/v1/mdm/groups", m.listGroups)
	h("GET /api/v1/mdm/groups/{id}", m.getGroup)
	h("DELETE /api/v1/mdm/groups/{id}", m.deleteGroup)
	h("GET /api/v1/mdm/groups/{id}/members", m.members)
	h("POST /api/v1/mdm/groups/{id}/members/{member}", m.member(true))
	h("DELETE /api/v1/mdm/groups/{id}/members/{member}", m.member(false))
	h("GET /api/v1/mdm/groups/{id}/profiles", m.groupProfiles)
	h("POST /api/v1/mdm/groups/{id}/profiles", m.assign(true))
	h("DELETE /api/v1/mdm/groups/{id}/profiles", m.assign(false))
	h("GET /api/v1/mdm/devices", m.listDevices)
	m.server = httptest.NewServer(mux)
	f.t.Cleanup(m.server.Close)
	return m
}

func mdmError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]any{"error_code": code, "error_description": desc, "localized_error_description": desc})
}

func idOf(r *http.Request, name string) int64 {
	n, _ := strconv.ParseInt(r.PathValue(name), 10, 64)
	return n
}

func (m *fakeMDM) id() int64 { m.nextID++; return m.nextID }

func (m *fakeMDM) upload(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Accept") == "application/json" {
		writeJSON(w, http.StatusNotAcceptable, map[string]any{"errorCode": "406", "errorMsg": "Not Acceptable"})
		return
	}
	if r.Header.Get("Module") != "MDM_CUSTOM_PROFILE" {
		mdmError(w, http.StatusBadRequest, "COM0005", "Module header missing")
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		mdmError(w, http.StatusBadRequest, "COM0005", "file missing")
		return
	}
	defer func() { _ = file.Close() }()
	data, _ := io.ReadAll(file)
	id := m.id()
	m.files[id] = string(data)
	writeJSON(w, http.StatusOK, []map[string]any{{"fileID": strconv.FormatInt(id, 10), "fileName": hdr.Filename, "fileStatus": 2}})
}

func (p *fakeMDMProfile) json(m *fakeMDM) map[string]any {
	status := "Yet To Deploy"
	if p.Version > 0 {
		status = "Published"
	}
	groups := 0
	for _, g := range m.groups {
		if slices.Contains(g.Profiles, p.ID) {
			groups++
		}
	}
	payloads := []string{}
	if p.PayloadItem != 0 {
		payloads = []string{"maccustomprofiles"}
	}
	return map[string]any{"profile_id": strconv.FormatInt(p.ID, 10), "profile_name": p.Name, "profile_description": p.Description,
		"platform_type": strconv.Itoa(p.Platform), "profile_status": status, "profile_version": strconv.Itoa(max(p.Version, 1)),
		"is_moved_to_trash": p.Trashed, "trashed": map[bool]string{true: "1", false: "0"}[p.Trashed], "payloads": payloads,
		"profile_associated_groups_count": strconv.Itoa(groups), "profile_associated_devices_count": "0"}
}

func (m *fakeMDM) createProfile(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name        string `json:"profile_name"`
		Description string `json:"profile_description"`
		Platform    int    `json:"platform_type"`
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	for _, p := range m.profiles {
		if p.Name == b.Name {
			mdmError(w, http.StatusConflict, "COM0010", "Resource Already Exists : profile_name - "+b.Name)
			return
		}
	}
	p := &fakeMDMProfile{ID: m.id(), Name: b.Name, Description: b.Description, Platform: b.Platform}
	m.profiles[p.ID] = p
	writeJSON(w, http.StatusOK, p.json(m))
}

func (m *fakeMDM) listProfiles(w http.ResponseWriter, _ *http.Request) {
	out := []map[string]any{}
	for _, p := range m.profiles {
		out = append(out, p.json(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": out})
}

// profile answers like the live API: an unknown or trashed profile is a 500.
func (m *fakeMDM) profile(w http.ResponseWriter, r *http.Request) *fakeMDMProfile {
	p, ok := m.profiles[idOf(r, "id")]
	if !ok || p.Trashed {
		mdmError(w, http.StatusInternalServerError, "COM0004", "Internal Server error, Please try again in a moment.")
		return nil
	}
	return p
}

func (m *fakeMDM) getProfile(w http.ResponseWriter, r *http.Request) {
	if p := m.profile(w, r); p != nil {
		writeJSON(w, http.StatusOK, p.json(m))
	}
}

func (m *fakeMDM) updateProfile(w http.ResponseWriter, r *http.Request) {
	p := m.profile(w, r)
	if p == nil {
		return
	}
	var b struct {
		Name        *string `json:"profile_name"`
		Description *string `json:"profile_description"`
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	if b.Name != nil {
		p.Name = *b.Name
	}
	if b.Description != nil {
		p.Description = *b.Description
	}
	w.WriteHeader(http.StatusAccepted)
}

func (m *fakeMDM) getPayload(w http.ResponseWriter, r *http.Request) {
	p := m.profile(w, r)
	if p == nil {
		return
	}
	items := []string{}
	if p.PayloadItem != 0 {
		items = []string{strconv.FormatInt(p.PayloadItem, 10)}
	}
	writeJSON(w, http.StatusOK, map[string]any{"payload_name": "maccustomprofiles", "payload_type": "767", "payloaditems": items})
}

func (m *fakeMDM) setPayload(modify bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := m.profile(w, r)
		if p == nil {
			return
		}
		if p.Platform != 6 {
			mdmError(w, http.StatusNotFound, "COM0008", "Unknown id(s) - [maccustomprofiles not belongs to this platform type "+strconv.Itoa(p.Platform)+"]")
			return
		}
		if modify && idOf(r, "item") != p.PayloadItem {
			mdmError(w, http.StatusNotFound, "COM0008", "Unknown id(s) - [ payloaditem_id - "+r.PathValue("item")+"]")
			return
		}
		if !modify && p.PayloadItem != 0 {
			mdmError(w, http.StatusConflict, "COM0010", "payload already exists")
			return
		}
		var b struct {
			FileID        int64 `json:"custom_profile_file_id"`
			AllowExisting bool  `json:"allow_existing_payload"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		content, ok := m.files[b.FileID]
		if !ok {
			mdmError(w, http.StatusBadRequest, "COM0005", "invalid custom_profile_file_id")
			return
		}
		if t := mdmSupportedPayload(content); t != "" && !b.AllowExisting {
			mdmError(w, http.StatusBadRequest, "PAY0011", "Payloads supported by MDM cannot be added. Payloads [["+t+"]].")
			return
		}
		p.Content, p.PayloadItem = content, m.id() // a new item ID on every edit
		writeJSON(w, http.StatusOK, map[string]any{"payload_id": strconv.FormatInt(p.PayloadItem, 10), "custom_profile_type": "1"})
	}
}

func (m *fakeMDM) publish(w http.ResponseWriter, r *http.Request) {
	p := m.profile(w, r)
	if p == nil {
		return
	}
	if p.Version == 0 || p.Content != p.Published {
		p.Version++
		p.Published = p.Content
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *fakeMDM) updateAll(w http.ResponseWriter, r *http.Request) {
	if p := m.profile(w, r); p != nil {
		m.updateAlls++
		w.WriteHeader(http.StatusAccepted)
	}
}

func profileIDs(r *http.Request) []int64 {
	var b struct {
		IDs []int64 `json:"profile_ids"`
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	return b.IDs
}

func (m *fakeMDM) trash(w http.ResponseWriter, r *http.Request) {
	for _, id := range profileIDs(r) {
		if p, ok := m.profiles[id]; ok {
			p.Trashed = true
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *fakeMDM) deleteProfiles(w http.ResponseWriter, r *http.Request) {
	ids := profileIDs(r)
	for _, id := range ids {
		if p, ok := m.profiles[id]; ok && !p.Trashed {
			mdmError(w, http.StatusPreconditionFailed, "COM0015", "Precondition Failed - Reason - Profiles ["+strconv.FormatInt(id, 10)+"] must be moved to trash before deleting it permanently")
			return
		}
	}
	for _, id := range ids {
		delete(m.profiles, id)
		for _, g := range m.groups {
			g.Profiles = slices.DeleteFunc(g.Profiles, func(p int64) bool { return p == id })
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (g *fakeMDMGroup) json() map[string]any {
	desc := g.Description
	if desc == "" {
		desc = "--"
	}
	return map[string]any{"group_id": strconv.FormatInt(g.ID, 10), "name": g.Name, "description": desc, "group_type": "6", "domain": "MDM"}
}

func (m *fakeMDM) createGroup(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		GroupType   int    `json:"group_type"`
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	if b.Name == "" || b.GroupType != 6 {
		mdmError(w, http.StatusBadRequest, "COM0005", "name and group_type 6 are required")
		return
	}
	g := &fakeMDMGroup{ID: m.id(), Name: b.Name, Description: b.Description}
	m.groups[g.ID] = g
	writeJSON(w, http.StatusOK, g.json())
}

func (m *fakeMDM) group(w http.ResponseWriter, r *http.Request) *fakeMDMGroup {
	g, ok := m.groups[idOf(r, "id")]
	if !ok {
		mdmError(w, http.StatusNotFound, "COM0008", "Unknown id(s) - [group_id: "+r.PathValue("id")+"]")
		return nil
	}
	return g
}

func (m *fakeMDM) listGroups(w http.ResponseWriter, _ *http.Request) {
	out := []map[string]any{}
	for _, g := range m.groups {
		out = append(out, g.json())
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": out})
}

func (m *fakeMDM) getGroup(w http.ResponseWriter, r *http.Request) {
	if g := m.group(w, r); g != nil {
		writeJSON(w, http.StatusOK, g.json())
	}
}

func (m *fakeMDM) deleteGroup(w http.ResponseWriter, r *http.Request) {
	if g := m.group(w, r); g != nil {
		delete(m.groups, g.ID)
		w.WriteHeader(http.StatusNoContent)
	}
}

func strIDs(ids []int64) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = strconv.FormatInt(id, 10)
	}
	return out
}

func (m *fakeMDM) members(w http.ResponseWriter, r *http.Request) {
	if g := m.group(w, r); g != nil {
		writeJSON(w, http.StatusOK, map[string]any{"member_ids": strIDs(g.Members)})
	}
}

func (m *fakeMDM) member(add bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		g := m.group(w, r)
		if g == nil {
			return
		}
		dev := idOf(r, "member")
		if !slices.ContainsFunc(m.devices, func(d fakeMDMDevice) bool { return d.ID == dev && !d.Removed }) {
			mdmError(w, http.StatusNotFound, "COM0008", "Unknown id(s) - [member_id: "+r.PathValue("member")+"]")
			return
		}
		g.Members = slices.DeleteFunc(g.Members, func(d int64) bool { return d == dev })
		if add {
			g.Members = append(g.Members, dev)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (m *fakeMDM) groupProfiles(w http.ResponseWriter, r *http.Request) {
	if g := m.group(w, r); g != nil {
		writeJSON(w, http.StatusOK, map[string]any{"profiles": strIDs(g.Profiles)})
	}
}

func (m *fakeMDM) assign(add bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		g := m.group(w, r)
		if g == nil {
			return
		}
		for _, id := range profileIDs(r) {
			p, ok := m.profiles[id]
			if !ok || p.Trashed {
				mdmError(w, http.StatusNotFound, "COM0008", "Unknown id(s) - [profile_id: "+strconv.FormatInt(id, 10)+"]")
				return
			}
			g.Profiles = slices.DeleteFunc(g.Profiles, func(x int64) bool { return x == id })
			if add {
				g.Profiles = append(g.Profiles, id)
			}
		}
		w.WriteHeader(http.StatusAccepted)
	}
}

// listDevices serves one device per page, to exercise the skip-token paging.
func (m *fakeMDM) listDevices(w http.ResponseWriter, r *http.Request) {
	i, _ := strconv.Atoi(r.URL.Query().Get("skip-token"))
	if i >= len(m.devices) {
		writeJSON(w, http.StatusOK, map[string]any{"devices": []any{}})
		return
	}
	d := m.devices[i]
	out := map[string]any{"devices": []map[string]any{{"device_id": strconv.FormatInt(d.ID, 10), "device_name": d.Name,
		"serial_number": d.Serial, "model": d.Model, "platform_type": "ios", "platform_type_id": "1", "device_type": "3",
		"os_version": "27.2", "udid": "UDID-" + d.Serial, "managed_status": map[bool]string{false: "2", true: "11"}[d.Removed],
		"is_removed": strconv.FormatBool(d.Removed)}}}
	if i+1 < len(m.devices) {
		out["paging"] = map[string]any{"next": m.server.URL + "/api/v1/mdm/devices?skip-token=" + strconv.Itoa(i+1)}
	}
	writeJSON(w, http.StatusOK, out)
}

// profileByName is a test helper (callers must not hold the lock).
func (m *fakeMDM) profileByName(name string) *fakeMDMProfile {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.profiles {
		if p.Name == name && !p.Trashed {
			cp := *p
			return &cp
		}
	}
	return nil
}

func (m *fakeMDM) groupByName(name string) *fakeMDMGroup {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, g := range m.groups {
		if strings.EqualFold(g.Name, name) {
			cp := *g
			return &cp
		}
	}
	return nil
}

// mdmSupportedPayloads are payload types the live API refuses in a custom
// profile unless allow_existing_payload is set (seen on a tenant).
var mdmSupportedPayloads = []string{"com.apple.MCX", "com.apple.applicationaccess", "com.apple.loginwindow",
	"com.apple.mobiledevice.passwordpolicy", "com.apple.screensaver", "com.apple.security.firewall",
	"com.apple.systempolicy.control", "com.apple.systempolicy.managed", "com.apple.systempreferences",
	"com.apple.vpn.managed", "com.apple.system-extension-policy"}

var payloadTypeRE = regexp.MustCompile(`<key>PayloadType</key>\s*<string>([^<]+)</string>`)

// mdmSupportedPayload returns the first payload type in content that the MDM
// has its own form for, or "".
func mdmSupportedPayload(content string) string {
	for _, m := range payloadTypeRE.FindAllStringSubmatch(content, -1) {
		if slices.Contains(mdmSupportedPayloads, m[1]) {
			return m[1]
		}
	}
	return ""
}
