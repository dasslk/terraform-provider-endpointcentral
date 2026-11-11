package provider

import (
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
