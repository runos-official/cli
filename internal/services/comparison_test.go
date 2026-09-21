package services

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/dynacmd"
	"github.com/runos-official/cli/internal/manifest"
)

// This fixture keeps the relevant input fields from the served account
// manifest 49.4.0+provider.spot.virt, captured on 2026-09-21.
// The full response has SHA-256
// 0186391184b6f7d6c4224bfb679390e8c382e28a0e6a541686bb5549fd7fae4a.
func productionClearableManifest(t *testing.T) *manifest.Manifest {
	t.Helper()
	raw, err := os.ReadFile("testdata/manifest-clearable-update-49.4.0.json")
	if err != nil {
		t.Fatal(err)
	}
	var m manifest.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return &m
}

func loadComparisonYAML(t *testing.T, serviceType, fields string) *ServiceYAML {
	t.Helper()
	path := filepath.Join(t.TempDir(), "service.yaml")
	content := "type: " + serviceType + "\nid: abc12\ncid: cluster1\naid: acct1\n" + fields
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	local, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return local
}

func TestBareClearableAffinityDoesNotEnterPatchOrPreview(t *testing.T) {
	m := productionClearableManifest(t)
	for _, serviceType := range []string{"postgresql", "valkey", "mysql", "vllm", "umami"} {
		t.Run(serviceType, func(t *testing.T) {
			update, err := UpdateCommand(m, serviceType)
			if err != nil {
				t.Fatal(err)
			}
			editName, editValue, editYAML := "replicas", any(2), "replicas: 2\n"
			if serviceType == "umami" {
				editName, editValue, editYAML = "name", "new-name", "name: new-name\n"
			}
			local := loadComparisonYAML(t, serviceType, "nodeAffinityTags:\n"+editYAML)
			server := &ServiceYAML{Type: serviceType, ID: "abc12", CID: "cluster1", AID: "acct1", Fields: map[string]any{"nodeAffinityTags": []any{"gpu:shared"}, editName: any(1)}}
			if serviceType == "umami" {
				server.Fields[editName] = "old-name"
			}
			plan := ComputeSyncPlan(local, server, nil, update, nil)
			if want := map[string]any{editName: editValue}; !reflect.DeepEqual(plan.PatchBody, want) {
				t.Fatalf("patch = %#v, want %#v", plan.PatchBody, want)
			}
			if len(plan.Removals) != 0 || strings.Contains(plan.Diff, "nodeAffinityTags") {
				t.Fatalf("bare pin appeared in preview: %#v", plan)
			}
		})
	}
}

func TestClearableAffinityYAMLFormsAndRequests(t *testing.T) {
	m := productionClearableManifest(t)
	forms := map[string]string{
		"bare":      "nodeAffinityTags:\n",
		"null":      "nodeAffinityTags: null\n",
		"Null":      "nodeAffinityTags: Null\n",
		"NULL":      "nodeAffinityTags: NULL\n",
		"tilde":     "nodeAffinityTags: ~\n",
		"short tag": "nodeAffinityTags: !!null null\n",
		"full tag":  "nodeAffinityTags: !<tag:yaml.org,2002:null> null\n",
	}
	for _, serviceType := range []string{"postgresql", "valkey", "mysql", "vllm", "umami"} {
		update, err := UpdateCommand(m, serviceType)
		if err != nil {
			t.Fatal(err)
		}
		if f := ClearableFields(update)["nodeAffinityTags"]; f.Type != "array" {
			t.Fatalf("%s marker = %#v", serviceType, f)
		}
		editField, editYAML, editValue := "replicas", "replicas: 2\n", any(float64(2))
		if serviceType == "umami" {
			editField, editYAML, editValue = "name", "name: new-name\n", "new-name"
		}
		for label, form := range forms {
			t.Run(serviceType+"/"+label, func(t *testing.T) {
				local := loadComparisonYAML(t, serviceType, form+editYAML)
				if value, present := local.Fields["nodeAffinityTags"]; !present || value != nil {
					t.Fatalf("YAML null lost presence: %#v", local.Fields)
				}
				serverFields := map[string]any{"nodeAffinityTags": []any{"gpu:shared"}, editField: any(float64(1))}
				if serviceType == "umami" {
					serverFields[editField] = "old-name"
				}
				server := &ServiceYAML{Type: serviceType, ID: "abc12", CID: "cluster1", AID: "acct1", Fields: serverFields}
				localBefore, serverBefore := cloneFields(local.Fields), cloneFields(server.Fields)
				plan := ComputeSyncPlan(local, server, nil, update, nil)
				if want := map[string]any{editField: editValue}; !jsonEqual(plan.PatchBody, want) {
					t.Fatalf("patch = %#v, want %#v", plan.PatchBody, want)
				}
				if len(plan.Removals) != 0 || strings.Contains(plan.Diff, "nodeAffinityTags") {
					t.Fatalf("preview includes unchanged pin: %#v", plan)
				}
				if !reflect.DeepEqual(local.Fields, localBefore) || !reflect.DeepEqual(server.Fields, serverBefore) {
					t.Fatal("comparison changed an input map")
				}
				assertCapturedPatch(t, plan, update, map[string]any{editField: editValue})
			})
		}
	}
}

func assertCapturedPatch(t *testing.T, plan *SyncPlan, update *manifest.Command, want map[string]any) {
	t.Helper()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPatch || r.URL.Path != fmt.Sprintf("/acct1/cluster1/services/%s/abc12", plan.Type) {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Error(err)
		}
		if !reflect.DeepEqual(body, want) {
			t.Errorf("body = %#v, want %#v", body, want)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jobId":"job-1"}`)
	}))
	defer srv.Close()
	syncEnv(t, srv.URL)
	res, err := ApplySyncPlan(dynacmd.NewExecutor(srv.URL), plan, nil, update)
	if err != nil || res.JobID != "job-1" || calls != 1 {
		t.Fatalf("apply result=%#v err=%v calls=%d", res, err, calls)
	}
}

func TestClearableAffinityNoOpAndRemoval(t *testing.T) {
	m := productionClearableManifest(t)
	for _, serviceType := range []string{"postgresql", "valkey", "mysql", "vllm", "umami"} {
		update, err := UpdateCommand(m, serviceType)
		if err != nil {
			t.Fatal(err)
		}
		cases := []struct {
			name, localYAML string
			serverPin       any
			serverHasPin    bool
			wantPatch       map[string]any
			wantRemoval     bool
		}{
			{"bare only", "nodeAffinityTags:\n", []any{"gpu:shared"}, true, nil, false},
			{"bare from empty", "nodeAffinityTags:\n", []any{}, true, nil, false},
			{"bare from absent", "nodeAffinityTags:\n", nil, false, nil, false},
			{"unchanged", "nodeAffinityTags: [gpu:shared]\n", []any{"gpu:shared"}, true, nil, false},
			{"changed", "nodeAffinityTags: [gpu:other]\n", []any{"gpu:shared"}, true, map[string]any{"nodeAffinityTags": []any{"gpu:other"}}, false},
			{"explicit empty", "nodeAffinityTags: []\n", []any{"gpu:shared"}, true, map[string]any{"nodeAffinityTags": []any{}}, false},
			{"deleted", "", []any{"gpu:shared"}, true, map[string]any{"nodeAffinityTags": []any{}}, true},
			{"deleted from empty", "", []any{}, true, nil, false},
			{"deleted from null", "", nil, true, nil, false},
			{"deleted from absent", "", nil, false, nil, false},
		}
		for _, tc := range cases {
			t.Run(serviceType+"/"+tc.name, func(t *testing.T) {
				local := loadComparisonYAML(t, serviceType, tc.localYAML)
				serverFields := map[string]any{}
				if tc.serverHasPin {
					serverFields["nodeAffinityTags"] = tc.serverPin
				}
				server := &ServiceYAML{Type: serviceType, ID: "abc12", CID: "cluster1", AID: "acct1", Fields: serverFields}
				comparison := CompareServiceState(local, server, update, nil)
				plan := ComputeSyncPlan(local, server, nil, update, nil)
				if !jsonEqual(plan.PatchBody, tc.wantPatch) || !jsonEqual(comparison.PatchBody, tc.wantPatch) {
					t.Fatalf("patch = %#v, comparison = %#v, want %#v", plan.PatchBody, comparison.PatchBody, tc.wantPatch)
				}
				if got := len(plan.Removals) != 0; got != tc.wantRemoval {
					t.Fatalf("removals = %#v, want removal=%t", plan.Removals, tc.wantRemoval)
				}
				if tc.wantRemoval && (plan.Removals[0] != "nodeAffinityTags" || !strings.Contains(plan.Diff, "nodeAffinityTags")) {
					t.Fatalf("deletion preview = %#v", plan)
				}
				if tc.wantPatch == nil {
					if plan.HasChanges() || comparison.HasDrift || plan.Diff != "" {
						t.Fatalf("unexpected drift: %#v, comparison=%#v", plan, comparison)
					}
					var calls int
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
					defer srv.Close()
					if _, err := ApplySyncPlan(dynacmd.NewExecutor(srv.URL), plan, nil, update); err != nil || calls != 0 {
						t.Fatalf("no-op apply error=%v calls=%d", err, calls)
					}
					return
				}
				assertCapturedPatch(t, plan, update, tc.wantPatch)
			})
		}
	}
}

func TestClearableAffinityQuotedValuesAndMalformedTag(t *testing.T) {
	m := productionClearableManifest(t)
	update, err := UpdateCommand(m, "vllm")
	if err != nil {
		t.Fatal(err)
	}
	server := &ServiceYAML{Type: "vllm", ID: "abc12", CID: "cluster1", AID: "acct1", Fields: map[string]any{"nodeAffinityTags": []any{"gpu:shared"}}}
	for _, value := range []string{`"null"`, `''`, `""`} {
		t.Run(value, func(t *testing.T) {
			local := loadComparisonYAML(t, "vllm", "nodeAffinityTags: "+value+"\n")
			if _, ok := local.Fields["nodeAffinityTags"].(string); !ok {
				t.Fatalf("quoted value became null: %#v", local.Fields)
			}
			plan := ComputeSyncPlan(local, server, nil, update, nil)
			if plan.PatchBody["nodeAffinityTags"] != local.Fields["nodeAffinityTags"] {
				t.Fatalf("quoted value was filtered: %#v", plan.PatchBody)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(path, []byte("type: vllm\nid: abc12\ncid: cluster1\naid: acct1\nnodeAffinityTags: !<tag:yaml.org,2002:null null\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("malformed tagged null parsed without error")
	}
}

func TestComparisonKeepsNonClearableNullAndAllowedEmptyString(t *testing.T) {
	m := productionClearableManifest(t)
	update, err := UpdateCommand(m, "vllm")
	if err != nil {
		t.Fatal(err)
	}
	server := &ServiceYAML{Type: "vllm", ID: "abc12", CID: "cluster1", AID: "acct1", Fields: map[string]any{"image": "registry.invalid/vllm:v1"}}
	for _, tc := range []struct {
		name, yaml string
		want       any
	}{
		{"null", "image: null\n", nil},
		{"empty", "image: \"\"\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local := loadComparisonYAML(t, "vllm", tc.yaml)
			result := CompareServiceState(local, server, update, nil)
			value, present := result.PatchBody["image"]
			if !present || value != tc.want {
				t.Fatalf("image = %#v, present=%t", value, present)
			}
		})
	}
}

func TestInvalidAffinityValueReachesServerRefusal(t *testing.T) {
	m := productionClearableManifest(t)
	update, err := UpdateCommand(m, "vllm")
	if err != nil {
		t.Fatal(err)
	}
	local := loadComparisonYAML(t, "vllm", "nodeAffinityTags: \"null\"\n")
	server := &ServiceYAML{Type: "vllm", ID: "abc12", CID: "cluster1", AID: "acct1", Fields: map[string]any{"nodeAffinityTags": []any{"gpu:shared"}}}
	plan := ComputeSyncPlan(local, server, nil, update, nil)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["nodeAffinityTags"] != "null" {
			t.Errorf("invalid scalar did not reach API: %#v", body)
		}
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"nodeAffinityTags must be an array"}`)
	}))
	defer srv.Close()
	syncEnv(t, srv.URL)
	_, err = ApplySyncPlan(dynacmd.NewExecutor(srv.URL), plan, nil, update)
	if err == nil || !strings.Contains(err.Error(), "nodeAffinityTags") || calls != 1 {
		t.Fatalf("error=%v, calls=%d", err, calls)
	}
}
