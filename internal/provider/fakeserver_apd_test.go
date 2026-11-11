package provider

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
)

// Fake of the APD calls Endpoint Central's console makes, following what a
// live tenant answers (see client/patch.go).

type fakeAPDTask struct {
	ID         int64
	Collection int64
	Status     string         // "Active" or "suspended"
	Body       map[string]any // the writable settings as last saved
}

// apdReadOnly are fields a read returns but an update rejects.
var apdReadOnly = []string{"isTargetEditable", "isTechMapWithCG", "isSimpleTarget", "taskStatus", "taskId", "collectionId", "isTargetRemoved", "isTargetComputable"}

func (f *fakeEC) registerAPD(h func(string, string, string, http.HandlerFunc)) {
	h("POST /dcapi/patch/autopatchdeployment", "", "application/addTask.v1+json", notJSON(f.createAPD))
	h("GET /dcapi/patch/autopatchdeployment/{id}", "", "", notJSON(f.getAPD))
	h("PUT /dcapi/patch/autopatchdeployment/{id}", "", "application/addModifyTask.v1+json", notJSON(f.updateAPD))
	h("POST /dcapi/patch/autopatchdeployment/suspend", "", "application/suspend.v1+json", notJSON(f.apdAction("suspend")))
	h("POST /dcapi/patch/autopatchdeployment/resume", "", "application/resume.v1+json", notJSON(f.apdAction("resume")))
	h("DELETE /dcapi/patch/autopatchdeployment/delete", "", "application/deletetask.v1+json", notJSON(f.apdAction("delete")))
	h("GET /dcapi/patch/reports/apdTasks", "", "", notJSON(f.listAPD))
}

func invalidTaskID(w http.ResponseWriter) {
	writeJSON(w, http.StatusInternalServerError, map[string]any{"errorCode": "1003", "errorMsg": "Invalid TaskID"})
}

// validAPDBody checks what the live API checks: no read-only fields, labels
// on every target, a known deployment policy, at least one update.
func (f *fakeEC) validAPDBody(w http.ResponseWriter, raw []byte) (map[string]any, bool) {
	var b map[string]any
	if err := json.Unmarshal(raw, &b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"errorCode": "IAM0017", "errorMsg": "Request JSON could not be parsed."})
		return nil, false
	}
	for _, k := range apdReadOnly {
		if containsKey(b, k) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"errorCode": "IAM0018", "errorMsg": `Invalid parameter "` + k + `" detected.`})
			return nil, false
		}
	}
	if !allTargetsLabelled(b["targetData"]) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"errorCode": "1003", "errorMsg": "An internal error occurred while processing the request. Please try again later."})
		return nil, false
	}
	pol, _ := b["templateId"].(string)
	if !slices.ContainsFunc(f.policies, func(p map[string]any) bool { return p["tempID"] == pol }) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"errorCode": "50003", "errorMsg": "Invalid deployment policy " + pol})
		return nil, false
	}
	os, _ := b["osUpdates"].([]any)
	tp, _ := b["tpSeverities"].([]any)
	if len(os) == 0 && len(tp) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"errorCode": "50003", "errorMsg": "Select at least one update"})
		return nil, false
	}
	return b, true
}

func containsKey(v any, key string) bool {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if k == key || containsKey(x, key) {
				return true
			}
		}
	case []any:
		for _, x := range t {
			if containsKey(x, key) {
				return true
			}
		}
	}
	return false
}

func allTargetsLabelled(v any) bool {
	ok := true
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if _, has := t["idValue"]; has {
				if l, _ := t["label"].(string); l == "" {
					ok = false
				}
			}
			for _, x := range t {
				walk(x)
			}
		case []any:
			for _, x := range t {
				walk(x)
			}
		}
	}
	walk(v)
	return ok
}

func readAll(r *http.Request) []byte {
	b := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		b = append(b, buf[:n]...)
		if err != nil {
			return b
		}
	}
}

func (f *fakeEC) createAPD(w http.ResponseWriter, r *http.Request) {
	b, ok := f.validAPDBody(w, readAll(r))
	if !ok {
		return
	}
	for _, t := range f.apdTasks {
		if t.Body["label"] == b["label"] {
			writeJSON(w, http.StatusBadRequest, map[string]any{"errorCode": "50003", "errorMsg": "Task name already exists"})
			return
		}
	}
	f.nextAPDID++
	t := &fakeAPDTask{ID: 10000000000418000 + f.nextAPDID, Collection: 10000000000419000 + f.nextAPDID, Status: "Active", Body: b}
	f.apdTasks[t.ID] = t
	writeJSON(w, http.StatusOK, map[string]any{"taskMsg": `"` + b["label"].(string) + `" has been created successfully`,
		"configType": "Computer", "collectionId": strconv.FormatInt(t.Collection, 10), "addedUser": "admin@example.com", "collectionName": b["label"]})
}

// readView is what GET returns: the saved settings plus read-only fields,
// numbers as numbers, names on OS update categories, "--" for no description.
func (t *fakeAPDTask) readView() map[string]any {
	raw, _ := json.Marshal(t.Body)
	var v map[string]any
	_ = json.Unmarshal(raw, &v)
	if v["description"] == "" {
		v["description"] = "--"
	}
	if pid, ok := v["platformId"].(string); ok {
		n, _ := strconv.Atoi(pid)
		v["platformId"] = n
	}
	if ups, ok := v["osUpdates"].([]any); ok {
		for _, u := range ups {
			m := u.(map[string]any)
			m["name"] = map[float64]string{0: "Security", 1: "Non Security"}[m["id"].(float64)]
		}
	}
	if td, ok := v["targetData"].(map[string]any); ok {
		td["isTargetEditable"] = true
		td["isSimpleTarget"] = false
	}
	v["taskStatus"] = t.Status
	v["taskId"] = strconv.FormatInt(t.ID, 10)
	v["collectionId"] = strconv.FormatInt(t.Collection, 10)
	return v
}

func (f *fakeEC) getAPD(w http.ResponseWriter, r *http.Request) {
	t, ok := f.apdTasks[pathID(r)]
	if !ok {
		invalidTaskID(w)
		return
	}
	writeJSON(w, http.StatusOK, t.readView())
}

func (f *fakeEC) updateAPD(w http.ResponseWriter, r *http.Request) {
	b, ok := f.validAPDBody(w, readAll(r))
	if !ok {
		return
	}
	t, ok := f.apdTasks[pathID(r)]
	if !ok {
		invalidTaskID(w)
		return
	}
	t.Body = b
	f.apdModifies++
	writeJSON(w, http.StatusOK, map[string]any{"taskMsg": `"` + b["label"].(string) + `" has been updated successfully`,
		"collectionId": strconv.FormatInt(t.Collection, 10), "collectionName": b["label"]})
}

func (f *fakeEC) apdAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var ids []string
		if err := json.Unmarshal(readAll(r), &ids); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"errorCode": "IAM0017", "errorMsg": "Request JSON could not be parsed."})
			return
		}
		out := map[string]bool{}
		for _, s := range ids {
			id, _ := strconv.ParseInt(s, 10, 64)
			t, ok := f.apdTasks[id]
			out[s] = ok
			if !ok {
				continue
			}
			switch action {
			case "suspend":
				t.Status = "suspended"
			case "resume":
				t.Status = "Active"
			case "delete":
				delete(f.apdTasks, id)
			}
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func (f *fakeEC) listAPD(w http.ResponseWriter, _ *http.Request) {
	rows := []map[string]any{}
	ids := make([]int64, 0, len(f.apdTasks))
	for id := range f.apdTasks {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		t := f.apdTasks[id]
		rows = append(rows, map[string]any{"task_name": t.Body["label"], "task_id": strconv.FormatInt(t.ID, 10),
			"collection_id": strconv.FormatInt(t.Collection, 10), "template_id": t.Body["templateId"],
			"os_platform_name": "Mac", "os_platform_id": t.Body["platformId"], "total_target": "1"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"metadata": map[string]any{"limit": 100, "page": 1},
		"totalRecords": strconv.Itoa(len(rows)), "messageResponse": rows, "status": "success"})
}

// apdByName is a test helper (callers must not hold the lock).
func (f *fakeEC) apdByName(name string) *fakeAPDTask {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.apdTasks {
		if t.Body["label"] == name {
			cp := *t
			return &cp
		}
	}
	return nil
}
