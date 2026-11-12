package provider

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// Fakes for the /dcapi calls (automated patch deployment, deployment
// policies) and the computer list, following what a live tenant answers.

func (f *fakeEC) registerDCAPI(mux *http.ServeMux) {
	f.apdTasks = map[int64]*fakeAPDTask{}
	// The shape a live tenant returns, not the documented one.
	f.policies = []map[string]any{
		{"tempID": "7001", "tempName": "Deploy any time at the earliest", "platformScope": 1},
		{"tempID": "7002", "tempName": "Mac canary window", "platformScope": 2},
	}

	h := func(pattern, accept, contentType string, fn http.HandlerFunc) {
		mux.HandleFunc(pattern, f.auth(func(w http.ResponseWriter, r *http.Request) {
			if accept != "" && r.Header.Get("Accept") != accept {
				writeJSON(w, http.StatusNotAcceptable, map[string]any{"error_code": "INVALID_ACCEPT", "error_description": "expected Accept " + accept + ", got " + r.Header.Get("Accept")})
				return
			}
			if contentType != "" && r.Header.Get("Content-Type") != contentType {
				writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"error_code": "INVALID_CONTENT_TYPE", "error_description": "expected Content-Type " + contentType + ", got " + r.Header.Get("Content-Type")})
				return
			}
			fn(w, r)
		}))
	}

	f.registerAPD(h)
	f.registerConfigurations(h)
	h("GET /dcapi/patch/deploymentPolicy", "", "", notJSON(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"createdByOthers": f.policies[:1], "selfCreated": f.policies[1:],
			"systemCreated": []string{"7001"}, "defaultTempID": -1})
	}))
	h("GET /api/1.4/som/computers", "", "", f.listComputers)
}

func pathID(r *http.Request) int64 {
	n, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return n
}

func (f *fakeEC) listComputers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out := []map[string]any{}
	ids := make([]int64, 0, len(f.knownResources))
	for id := range f.knownResources {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		name := f.knownResources[id]
		fqdn := strings.ToLower(name) + ".corp.example"
		if v := q.Get("residfilter"); v != "" && v != strconv.FormatInt(id, 10) {
			continue
		}
		if v := q.Get("computernamefilter"); v != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(v)) {
			continue
		}
		if v := q.Get("fqdnfilter"); v != "" && !strings.EqualFold(v, fqdn) {
			continue
		}
		out = append(out, map[string]any{"resource_id": id, "resource_name": name, "fqdn_name": fqdn,
			"domain_netbios_name": "CORP", "os_platform_name": "Mac", "os_name": "macOS 27", "os_version": "27.1",
			"ip_address": "10.0.0." + strconv.FormatInt(id%250, 10), "mac_address": "aa:bb:cc:dd:ee:ff",
			"agent_version": "11.4.2522.01.M", "branch_office_name": "Local Office"})
	}
	legacyOK(w, "computers", map[string]any{"total": len(out), "limit": 1000, "page": 1, "computers": out})
}

// notJSON answers 406 to "Accept: application/json", like the live patch endpoints.
func notJSON(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") == "application/json" {
			writeJSON(w, http.StatusNotAcceptable, map[string]any{"errorCode": "406", "errorMsg": "Not Acceptable"})
			return
		}
		fn(w, r)
	}
}

// --- configurations ----------------------------------------------------------

type fakeConfig struct {
	ID      int64
	Body    map[string]any // as created
	Deleted bool
	Trashed bool
}

func (f *fakeEC) registerConfigurations(h func(string, string, string, http.HandlerFunc)) {
	f.configs = map[int64]*fakeConfig{}
	f.nextConfigID = 52501000000421000
	f.scripts = []map[string]any{
		{"scriptId": "52501000000401316", "scriptName": "set-computer-name.sh", "platformID": 2, "exitCodes": "0",
			"description": "--", "scriptArguments": "--prefix corp"},
		{"scriptId": "52501000000401400", "scriptName": "dup.sh", "platformID": 2, "exitCodes": "0", "description": "--"},
		{"scriptId": "52501000000401401", "scriptName": "dup.sh", "platformID": 2, "exitCodes": "0", "description": "--"},
		{"scriptId": "52501000000401500", "scriptName": "linux-setup.sh", "platformID": 3, "exitCodes": "0", "description": "Linux"},
	}
	h("GET /dcapi/configuration/scripts", "", "", f.listScripts)
	h("POST /dcapi/configuration/mac/customScript/deploy", "application/configDeployStatus.v1+json", "application/deployMacCustomScript.v1+json", f.createScriptConfig)
	h("GET /dcapi/configuration/{id}", "application/collectionDetails.v1+json", "", f.getConfig)
	// Modify refuses OAuth tokens on a live tenant, whatever the scopes.
	h("POST /dcapi/configuration/{id}/deploy", "", "", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"errorMsg": "INVALID_OAUTHSCOPE"})
	})
	h("POST /dcapi/configuration/moveToTrash", "application/moveConfigToTrashStatus.v1+json", "application/moveConfigToTrash.v1+json", f.trashConfigs)
	h("DELETE /dcapi/configuration/trash/delete", "application/deletedConfigStatus.v1+json", "application/deleteConfigTrash.v1+json", f.deleteTrashedConfigs)
}

func (f *fakeEC) listScripts(w http.ResponseWriter, r *http.Request) {
	ids := map[string]float64{"windows": 1, "mac": 2, "linux": 3}
	pid, ok := ids[r.URL.Query().Get("platform")]
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"errorCode": "IAM0025", "errorMsg": "platform is an invalid parameter format."})
		return
	}
	out := []map[string]any{}
	for _, s := range f.scripts {
		if n, _ := s["platformID"].(int); float64(n) == pid {
			out = append(out, s)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (f *fakeEC) createScriptConfig(w http.ResponseWriter, r *http.Request) {
	var b map[string]any
	if err := json.Unmarshal(readAll(r), &b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"errorCode": "IAM0017", "errorMsg": "Request JSON could not be parsed."})
		return
	}
	details, _ := b["configDetails"].([]any)
	if len(details) == 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"errorCode": "GENERIC0002", "errorMsg": `Cannot invoke "java.util.ArrayList.size()" because "configDetailsList" is null`})
		return
	}
	if !allTargetsLabelled(b["targetData"]) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"errorCode": "1003", "errorMsg": "An internal error occurred while processing the request."})
		return
	}
	f.nextConfigID += 2
	c := &fakeConfig{ID: f.nextConfigID, Body: b}
	f.configs[c.ID] = c
	writeJSON(w, http.StatusOK, map[string]any{"collectionName": b["label"], "collectionId": strconv.FormatInt(c.ID, 10),
		"addedUser": "admin@example.com", "configType": "computer"})
}

// view is what a read returns: the stored body plus read-only fields, the
// script entry with its detailId, targets with their flags.
func (c *fakeConfig) view() map[string]any {
	raw, _ := json.Marshal(c.Body)
	var v map[string]any
	_ = json.Unmarshal(raw, &v)
	v["collectionId"] = strconv.FormatInt(c.ID, 10)
	v["configType"] = "Computer"
	v["status"] = "In Progress"
	v["state"] = 0
	if v["description"] == "" {
		v["description"] = ""
	}
	for _, cd := range v["configDetails"].([]any) {
		for _, d := range cd.(map[string]any)["details"].([]any) {
			dm := d.(map[string]any)
			dm["detailId"] = strconv.FormatInt(c.ID+1, 10)
			dm["useCredential"] = 1
			dm["execCount"] = 0
			delete(dm, "credentialDetails")
			delete(dm, "dependencyFiles")
			delete(dm, "command")
			delete(dm, "updateStatus")
		}
	}
	if td, ok := v["targetData"].(map[string]any); ok {
		td["isTargetEditable"] = true
		td["isSimpleTarget"] = false
	}
	return v
}

func (f *fakeEC) getConfig(w http.ResponseWriter, r *http.Request) {
	c, ok := f.configs[pathID(r)]
	if !ok || c.Deleted {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"errorCode": "1001", "errorMsg": "Access denied - You are not allowed to access the requested resource."})
		return
	}
	writeJSON(w, http.StatusOK, c.view())
}

func collectionIDs(r *http.Request) []int64 {
	var b struct {
		IDs []json.Number `json:"collectionIds"`
	}
	_ = json.Unmarshal(readAll(r), &b)
	out := []int64{}
	for _, n := range b.IDs {
		if i, err := n.Int64(); err == nil {
			out = append(out, i)
		}
	}
	return out
}

func (f *fakeEC) trashConfigs(w http.ResponseWriter, r *http.Request) {
	names := []any{}
	for _, id := range collectionIDs(r) {
		if c, ok := f.configs[id]; ok && !c.Deleted {
			c.Trashed = true
			names = append(names, c.Body["label"])
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "collectionName": names})
}

func (f *fakeEC) deleteTrashedConfigs(w http.ResponseWriter, r *http.Request) {
	for _, id := range collectionIDs(r) {
		if c, ok := f.configs[id]; ok && c.Trashed {
			c.Deleted = true
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success"})
}

// liveConfigs returns the configurations that are neither trashed nor
// deleted (callers must not hold the lock).
func (f *fakeEC) liveConfigs() []*fakeConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []*fakeConfig{}
	for _, c := range f.configs {
		if !c.Trashed && !c.Deleted {
			cp := *c
			out = append(out, &cp)
		}
	}
	return out
}
