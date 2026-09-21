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

// The fixture selects service add commands from an authenticated manifest GET.
// Source: account-scoped dev manifest, 2026-09-21 11:03:50 UTC.
// Version: 49.4.0+provider.spot.virt.
// Full response SHA-256: 0186391184b6f7d6c4224bfb679390e8c382e28a0e6a541686bb5549fd7fae4a.
// Select services/<type>/add commands declaring nodeAffinityTags. Keep their
// command, endpoint, method, and input field names, types, and gate metadata.
func createAffinityManifest(t *testing.T) *manifest.Manifest {
	t.Helper()
	raw, err := os.ReadFile("testdata/manifest-create-affinity-49.4.0.json")
	if err != nil {
		t.Fatal(err)
	}
	var m manifest.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return &m
}

func createAffinityYAML(t *testing.T, serviceType, affinity string, required []manifest.Field) (*ServiceYAML, map[string]any) {
	t.Helper()
	fields := map[string]any{"name": "test-service"}
	var body strings.Builder
	fmt.Fprintf(&body, "type: %s\ncid: %s\naid: %s\nname: test-service\n", serviceType, syncTestClusterID, syncTestAccountID)
	for _, field := range required {
		value := "example-value"
		switch field.Name {
		case "model":
			value = "example/model"
		case "bmcInterface":
			value = "eth0"
		case "postgresOsid":
			value = "postgresql-abc12"
		default:
			if strings.HasSuffix(field.Name, "Osid") {
				value = strings.TrimSuffix(strings.ToLower(field.Name), "osid") + "-abc12"
			}
		}
		fmt.Fprintf(&body, "%s: %s\n", field.Name, value)
		fields[field.Name] = value
	}
	body.WriteString(affinity)
	path := filepath.Join(t.TempDir(), "service.yaml")
	if err := os.WriteFile(path, []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	local, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return local, fields
}

func TestCreateAffinityNullFormsOmitWireField(t *testing.T) {
	m := createAffinityManifest(t)
	forms := map[string]string{
		"bare":      "nodeAffinityTags:\n",
		"null":      "nodeAffinityTags: null\n",
		"Null":      "nodeAffinityTags: Null\n",
		"NULL":      "nodeAffinityTags: NULL\n",
		"tilde":     "nodeAffinityTags: ~\n",
		"short tag": "nodeAffinityTags: !!null null\n",
		"full tag":  "nodeAffinityTags: !<tag:yaml.org,2002:null> null\n",
	}
	if len(m.Commands) != 22 {
		t.Fatalf("create inventory has %d commands, want 22", len(m.Commands))
	}
	for _, command := range m.Commands {
		serviceType := strings.TrimSuffix(strings.TrimPrefix(command.Command, "services/"), "/add")
		add, err := AddCommand(m, serviceType)
		if err != nil {
			t.Fatal(err)
		}
		var affinity *manifest.Field
		var required []manifest.Field
		for i := range add.Input.Fields {
			field := &add.Input.Fields[i]
			if field.Name == "nodeAffinityTags" {
				affinity = field
			} else if field.Required {
				required = append(required, *field)
			}
		}
		if affinity == nil || affinity.Type != "array" || affinity.Required || affinity.Positional {
			t.Fatalf("%s has unexpected affinity contract: %#v", add.Command, affinity)
		}
		for label, form := range forms {
			t.Run(serviceType+"/"+label, func(t *testing.T) {
				local, want := createAffinityYAML(t, serviceType, form, required)
				if value, present := local.Fields["nodeAffinityTags"]; !present || value != nil {
					t.Fatalf("YAML null lost presence: %#v", local.Fields)
				}
				before := cloneFields(local.Fields)
				plan := ComputeSyncPlan(local, nil, add, nil, nil)
				if !reflect.DeepEqual(local.Fields, before) || !reflect.DeepEqual(plan.CreateBody, want) || len(plan.Refused) != 0 {
					t.Fatalf("create plan = %#v; local = %#v; want body = %#v", plan, local.Fields, want)
				}
				var calls int
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if r.Method != http.MethodPost || r.URL.Path != "/"+syncTestAccountID+"/"+syncTestClusterID+"/services/"+serviceType {
						t.Errorf("request = %s %s", r.Method, r.URL.Path)
					}
					raw, _ := io.ReadAll(r.Body)
					var got map[string]any
					if err := json.Unmarshal(raw, &got); err != nil || !jsonEqual(got, want) {
						t.Errorf("request body = %s, want %#v; decode error = %v", raw, want, err)
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(w, `{"osid":%q,"jobId":"job-1"}`, serviceType+"-abc12")
				}))
				t.Cleanup(srv.Close)
				syncEnv(t, srv.URL)
				result, err := ApplySyncPlan(dynacmd.NewExecutor(srv.URL), plan, add, nil)
				if err != nil || calls != 1 || result == nil || result.NewID != "abc12" || result.JobID != "job-1" {
					t.Fatalf("apply: result = %#v, calls = %d, error = %v", result, calls, err)
				}
			})
		}
	}
}

func TestCreateAffinityExplicitValuesReachWire(t *testing.T) {
	m := createAffinityManifest(t)
	add, err := AddCommand(m, "valkey")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, yaml string
		want       map[string]any
	}{
		{"missing", "", map[string]any{"name": "test-service"}},
		{"empty array", "nodeAffinityTags: []\n", map[string]any{"name": "test-service", "nodeAffinityTags": []any{}}},
		{"nonempty array", "nodeAffinityTags: [gpu:shared]\n", map[string]any{"name": "test-service", "nodeAffinityTags": []any{"gpu:shared"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local, _ := createAffinityYAML(t, "valkey", tc.yaml, nil)
			before := cloneFields(local.Fields)
			plan := ComputeSyncPlan(local, nil, add, nil, nil)
			if !reflect.DeepEqual(local.Fields, before) || !reflect.DeepEqual(plan.CreateBody, tc.want) {
				t.Fatalf("plan = %#v; original = %#v", plan.CreateBody, local.Fields)
			}
			var calls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/"+syncTestAccountID+"/"+syncTestClusterID+"/services/valkey" {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !jsonEqual(body, tc.want) {
					t.Errorf("request body = %#v, error = %v", body, err)
				}
				fmt.Fprint(w, `{"id":"abc12","jobId":"job-1"}`)
			}))
			t.Cleanup(srv.Close)
			syncEnv(t, srv.URL)
			result, err := ApplySyncPlan(dynacmd.NewExecutor(srv.URL), plan, add, nil)
			if err != nil || calls != 1 || result.NewID != "abc12" {
				t.Fatalf("apply: result = %#v, calls = %d, error = %v", result, calls, err)
			}
		})
	}
}

func TestCreateAffinityQuotedScalarsKeepAPIRefusal(t *testing.T) {
	m := createAffinityManifest(t)
	add, _ := AddCommand(m, "valkey")
	for _, tc := range []struct{ label, yaml, value string }{
		{"quoted null", `nodeAffinityTags: "null"` + "\n", "null"},
		{"quoted empty", `nodeAffinityTags: ""` + "\n", ""},
	} {
		t.Run(tc.label, func(t *testing.T) {
			local, _ := createAffinityYAML(t, "valkey", tc.yaml, nil)
			plan := ComputeSyncPlan(local, nil, add, nil, nil)
			if plan.CreateBody["nodeAffinityTags"] != tc.value {
				t.Fatalf("create body = %#v", plan.CreateBody)
			}
			var calls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/"+syncTestAccountID+"/"+syncTestClusterID+"/services/valkey" {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !jsonEqual(body, plan.CreateBody) {
					t.Errorf("request body = %#v, error = %v", body, err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":"nodeAffinityTags must be an array"}`)
			}))
			t.Cleanup(srv.Close)
			syncEnv(t, srv.URL)
			_, err := ApplySyncPlan(dynacmd.NewExecutor(srv.URL), plan, add, nil)
			if calls != 1 || err == nil || !strings.Contains(err.Error(), "nodeAffinityTags must be an array") {
				t.Fatalf("controlled refusal: calls = %d, error = %v", calls, err)
			}
		})
	}
}

func TestNormalizeCreateAffinityContractGuards(t *testing.T) {
	for _, tc := range []struct {
		name      string
		field     *manifest.Field
		wantOmit  bool
		wantBlock bool
	}{
		{"optional array", &manifest.Field{Name: "nodeAffinityTags", Type: "array"}, true, false},
		{"required array", &manifest.Field{Name: "nodeAffinityTags", Type: "array", Required: true}, false, false},
		{"positional array", &manifest.Field{Name: "nodeAffinityTags", Type: "array", Positional: true}, false, true},
		{"unexpected type", &manifest.Field{Name: "nodeAffinityTags", Type: "string"}, false, false},
		{"undeclared", nil, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := []manifest.Field{{Name: "name", Type: "string"}, {Name: "image", Type: "string"}}
			if tc.field != nil {
				fields = append(fields, *tc.field)
			}
			add := &manifest.Command{Input: &manifest.Input{Fields: fields}}
			nested := map[string]any{"one": "two"}
			local := &ServiceYAML{Type: "synthetic", CID: syncTestClusterID, AID: syncTestAccountID, Fields: map[string]any{
				"name": "test-service", "nodeAffinityTags": nil, "image": nil, "nested": nested,
			}}
			before := cloneFields(local.Fields)
			normalized := normalizeCreateAffinity(local.Fields, add)
			_, normalizedPresent := normalized["nodeAffinityTags"]
			if normalizedPresent == tc.wantOmit {
				t.Fatalf("normalized fields = %#v", normalized)
			}
			plan := ComputeSyncPlan(local, nil, add, nil, nil)
			_, present := plan.CreateBody["nodeAffinityTags"]
			if present != (!tc.wantOmit && !tc.wantBlock) || plan.CreateBody["image"] != nil || !reflect.DeepEqual(local.Fields, before) {
				t.Fatalf("plan = %#v; local = %#v", plan, local.Fields)
			}
			if len(plan.Refused) == 0 {
				t.Fatalf("nested unknown field was not refused: %#v", plan.Refused)
			}
			if tc.wantBlock && !strings.Contains(strings.Join(plan.Refused, " "), "nodeAffinityTags") {
				t.Fatalf("affinity refusal missing: %#v", plan.Refused)
			}
			if _, ok := local.Fields["nodeAffinityTags"]; !ok {
				t.Fatal("normalization mutated the caller's fields")
			}
			if nested["one"] != "two" {
				t.Fatal("normalization mutated a nested value")
			}
		})
	}
}

func TestCreateAffinityKeepsOtherNullAndSupportedEmptyString(t *testing.T) {
	add := &manifest.Command{Input: &manifest.Input{Fields: []manifest.Field{
		{Name: "name", Type: "string"},
		{Name: "nodeAffinityTags", Type: "array"},
		{Name: "modelSource", Type: "object"},
		{Name: "placementNote", Type: "string", AllowEmpty: true},
	}}}
	local := &ServiceYAML{Type: "synthetic", CID: syncTestClusterID, AID: syncTestAccountID, Fields: map[string]any{
		"name": "test-service", "nodeAffinityTags": nil, "modelSource": nil, "placementNote": "",
	}}
	plan := ComputeSyncPlan(local, nil, add, nil, nil)
	want := map[string]any{"name": "test-service", "modelSource": nil, "placementNote": ""}
	if !reflect.DeepEqual(plan.CreateBody, want) || len(plan.Refused) != 0 {
		t.Fatalf("create body = %#v; refused = %#v", plan.CreateBody, plan.Refused)
	}
}

func TestCreateAffinityOnlyFieldStillPostsAndSavesID(t *testing.T) {
	m := createAffinityManifest(t)
	add, err := AddCommand(m, "valkey")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, affinity string
		want           map[string]any
	}{
		{"bare", "nodeAffinityTags:\n", map[string]any{}},
		{"null", "nodeAffinityTags: null\n", map[string]any{}},
		{"empty array", "nodeAffinityTags: []\n", map[string]any{"nodeAffinityTags": []any{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "service.yaml")
			yaml := "type: valkey\ncid: " + syncTestClusterID + "\naid: " + syncTestAccountID + "\n" + tc.affinity
			if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			local, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			plan := ComputeSyncPlan(local, nil, add, nil, nil)
			if plan.CreateBody == nil || !plan.HasChanges() || !reflect.DeepEqual(plan.CreateBody, tc.want) {
				t.Fatalf("create intent lost: body = %#v, has changes = %t", plan.CreateBody, plan.HasChanges())
			}
			var calls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/"+syncTestAccountID+"/"+syncTestClusterID+"/services/valkey" {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read body: %v", err)
				}
				if len(tc.want) == 0 {
					if len(raw) != 0 {
						t.Errorf("empty create body = %q", raw)
					}
				} else {
					var body map[string]any
					if err := json.Unmarshal(raw, &body); err != nil || !jsonEqual(body, tc.want) {
						t.Errorf("request body = %q, error = %v", raw, err)
					}
				}
				fmt.Fprint(w, `{"osid":"valkey-abc12","jobId":"job-1"}`)
			}))
			t.Cleanup(srv.Close)
			syncEnv(t, srv.URL)
			result, err := ApplySyncPlan(dynacmd.NewExecutor(srv.URL), plan, add, nil)
			if err != nil || calls != 1 || result == nil || result.NewID != "abc12" || result.JobID != "job-1" {
				t.Fatalf("apply: result = %#v, calls = %d, error = %v", result, calls, err)
			}
			local.ID = result.NewID
			if err := Save(path, local); err != nil {
				t.Fatal(err)
			}
			saved, err := Load(path)
			if err != nil || saved.ID != "abc12" {
				t.Fatalf("saved service = %#v, error = %v", saved, err)
			}
		})
	}
}
