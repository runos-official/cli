package roidapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// fakeRoid is a small in-memory roid access API built from the OpenAPI
// document's rules: ETags with If-Match (428 missing, 412 stale), a
// security-key step-up for escalation-class grants, cursor paging and
// roid's error body.
type fakeRoid struct {
	t       *testing.T
	mu      sync.Mutex
	token   string
	roles   map[string]*fakeVersioned
	groups  map[string]*fakeVersioned
	grants  map[string]*fakeVersioned
	stepUps map[string]bool   // completed step-up ids
	used    map[string]bool   // step-up ids already spent
	suState map[string]string // a forced poll state (expired), by id
	baseURL string            // set by newFakeRoid; the step-up page lives here
	// onPoll, when set, runs on every step-up poll (the browser finishing).
	onPoll  func(id string, polls int)
	polls   map[string]int
	calls   []string // "METHOD path" in order, for assertions
	headers []http.Header
	nextID  int
}

type fakeVersioned struct {
	version int
	body    map[string]any
}

func (v *fakeVersioned) etag() string { return fmt.Sprintf(`"v%d"`, v.version) }

func newFakeRoid(t *testing.T) (*fakeRoid, *httptest.Server) {
	f := &fakeRoid{t: t, token: "at_test", roles: map[string]*fakeVersioned{}, groups: map[string]*fakeVersioned{},
		grants: map[string]*fakeVersioned{}, stepUps: map[string]bool{}, used: map[string]bool{}, suState: map[string]string{}, polls: map[string]int{}}
	f.roles["viewer"] = &fakeVersioned{version: 1, body: map[string]any{"id": "viewer", "name": "Viewer", "builtin": true, "actions": []string{"clusters.read"}}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	f.baseURL = srv.URL
	t.Cleanup(srv.Close)
	return f, srv
}

func fakeErr(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": body})
}

func fakeJSON(w http.ResponseWriter, status int, etag string, v any) {
	w.Header().Set("Content-Type", "application/json")
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// checkMatch applies the If-Match rule to one existing resource.
func checkMatch(w http.ResponseWriter, r *http.Request, v *fakeVersioned) bool {
	m := r.Header.Get("If-Match")
	if m == "" {
		fakeErr(w, http.StatusPreconditionRequired, map[string]any{"code": "precondition_required"})
		return false
	}
	if m != v.etag() {
		fakeErr(w, http.StatusPreconditionFailed, map[string]any{"code": "precondition_failed"})
		return false
	}
	return true
}

func (f *fakeRoid) page(w http.ResponseWriter, set map[string]*fakeVersioned) {
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := []map[string]any{}
	for _, id := range ids {
		item := map[string]any{"etag": set[id].etag(), "version": set[id].version}
		for k, v := range set[id].body {
			item[k] = v
		}
		items = append(items, item)
	}
	fakeJSON(w, http.StatusOK, "", map[string]any{"items": items})
}

func (f *fakeRoid) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.EscapedPath())
	f.headers = append(f.headers, r.Header.Clone())
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		fakeErr(w, http.StatusUnauthorized, map[string]any{"code": "authn.invalid_credential"})
		return
	}
	if id, ok := strings.CutPrefix(r.URL.Path, "/cli/stepup/"); ok && r.Method == http.MethodGet {
		f.polls[id]++
		if f.onPoll != nil {
			f.onPoll(id, f.polls[id])
		}
		state := "pending"
		switch {
		case f.suState[id] != "":
			state = f.suState[id]
		case f.used[id]:
			state = "used"
		case f.stepUps[id]:
			state = "proven"
		}
		fakeJSON(w, http.StatusOK, "", map[string]any{"id": id, "state": state})
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/accounts/"), "/")
	if len(parts) < 3 || parts[1] != "access" {
		fakeErr(w, http.StatusNotFound, map[string]any{"code": "not_found"})
		return
	}
	var in map[string]any
	if r.Body != nil && (r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch) {
		_ = json.NewDecoder(r.Body).Decode(&in)
	}
	switch kind, rest := parts[2], parts[3:]; {
	case kind == "roles" && len(rest) == 0 && r.Method == http.MethodGet:
		f.page(w, f.roles)
	case kind == "roles" && len(rest) == 0 && r.Method == http.MethodPost:
		id, _ := in["id"].(string)
		f.roles[id] = &fakeVersioned{version: 1, body: in}
		fakeJSON(w, http.StatusCreated, f.roles[id].etag(), in)
	case kind == "roles" && len(rest) == 1:
		v, ok := f.roles[rest[0]]
		if !ok {
			fakeErr(w, http.StatusNotFound, map[string]any{"code": "not_found"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			fakeJSON(w, http.StatusOK, v.etag(), v.body)
		case http.MethodPut:
			if checkMatch(w, r, v) {
				v.version++
				v.body["name"], v.body["actions"] = in["name"], in["actions"]
				fakeJSON(w, http.StatusOK, v.etag(), v.body)
			}
		case http.MethodDelete:
			if checkMatch(w, r, v) {
				delete(f.roles, rest[0])
				w.WriteHeader(http.StatusNoContent)
			}
		}
	case kind == "groups" && len(rest) == 0 && r.Method == http.MethodGet:
		f.page(w, f.groups)
	case kind == "groups" && len(rest) == 0 && r.Method == http.MethodPost:
		id, _ := in["id"].(string)
		in["members"], in["idp_managed"] = []string{}, false
		f.groups[id] = &fakeVersioned{version: 1, body: in}
		fakeJSON(w, http.StatusCreated, f.groups[id].etag(), in)
	case kind == "groups" && len(rest) >= 1:
		v, ok := f.groups[rest[0]]
		if !ok {
			fakeErr(w, http.StatusNotFound, map[string]any{"code": "not_found"})
			return
		}
		switch {
		case len(rest) == 1 && r.Method == http.MethodGet:
			fakeJSON(w, http.StatusOK, v.etag(), v.body)
		case len(rest) == 1 && r.Method == http.MethodPatch:
			if checkMatch(w, r, v) {
				v.version++
				v.body["name"] = in["name"]
				fakeJSON(w, http.StatusOK, v.etag(), v.body)
			}
		case len(rest) == 1 && r.Method == http.MethodDelete:
			if checkMatch(w, r, v) {
				delete(f.groups, rest[0])
				w.WriteHeader(http.StatusNoContent)
			}
		case len(rest) == 3 && rest[1] == "members":
			if !checkMatch(w, r, v) {
				return
			}
			members, _ := v.body["members"].([]string)
			if r.Method == http.MethodPut {
				members = append(members, rest[2])
			} else {
				out := members[:0]
				for _, m := range members {
					if m != rest[2] {
						out = append(out, m)
					}
				}
				members = out
			}
			v.version++
			v.body["members"] = members
			fakeJSON(w, http.StatusOK, v.etag(), v.body)
		}
	case kind == "grants" && len(rest) == 0 && r.Method == http.MethodGet:
		f.page(w, f.grants)
	case kind == "grants" && len(rest) == 0 && r.Method == http.MethodPost:
		// Granting admin is escalation class: it needs a completed step-up.
		if role, _ := in["role"].(string); role == "admin" && (!f.stepUps[r.Header.Get("X-RunOS-Step-Up")] || f.used[r.Header.Get("X-RunOS-Step-Up")]) {
			body := map[string]any{"code": "authz.needs", "requirement": "step_up", "step_up_id": "su_1", "action": "access.grant"}
			if f.baseURL != "" {
				body["step_up_url"] = f.baseURL + "/stepup/su_1"
			}
			fakeErr(w, http.StatusForbidden, body)
			return
		}
		if su := r.Header.Get("X-RunOS-Step-Up"); su != "" {
			f.used[su] = true // a step-up is single use
		}
		f.nextID++
		id := fmt.Sprintf("g_%d", f.nextID)
		in["id"] = id
		f.grants[id] = &fakeVersioned{version: 1, body: in}
		fakeJSON(w, http.StatusCreated, f.grants[id].etag(), in)
	case kind == "grants" && len(rest) >= 1:
		v, ok := f.grants[rest[0]]
		if !ok {
			fakeErr(w, http.StatusNotFound, map[string]any{"code": "not_found"})
			return
		}
		switch {
		case len(rest) == 1 && r.Method == http.MethodGet:
			fakeJSON(w, http.StatusOK, v.etag(), v.body)
		case len(rest) == 1 && r.Method == http.MethodDelete:
			if checkMatch(w, r, v) {
				delete(f.grants, rest[0])
				w.WriteHeader(http.StatusNoContent)
			}
		case len(rest) == 2 && rest[1] == "audit":
			fakeJSON(w, http.StatusOK, "", map[string]any{"items": []map[string]any{{"seq": 1, "event": "access.grant.created", "at": "2026-10-16T00:00:00Z", "actor": "p_admin"}}})
		}
	case kind == "effective":
		fakeJSON(w, http.StatusOK, "", map[string]any{"person": r.URL.Query().Get("person"), "scope": r.URL.Query().Get("scope"),
			"policy_version": 7, "assumes": "", "actions": []map[string]any{{"action": "clusters.read", "class": "read", "outcome": "permit", "grant": "g_1", "via": "group:ops"}}})
	case kind == "actions":
		fakeJSON(w, http.StatusOK, "", map[string]any{"items": []map[string]any{{"id": "clusters.read", "class": "read", "scope_type": "cluster", "module": "core", "escalation": false}}})
	default:
		fakeErr(w, http.StatusNotFound, map[string]any{"code": "not_found"})
	}
}
