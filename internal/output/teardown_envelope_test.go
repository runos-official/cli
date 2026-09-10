package output

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

func TestTeardownEnvelopePreservesEnclosingFields(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want []string
	}{
		{"failed rollback", `{"id":"rollback-job","type":"cloudProviders.addServer","status":"failed","progress":"2/5","error":"Provisioning failed before rollback","result":{"detail":"Original provisioning result"},"teardowns":[` + teardownFixture + `]}`, []string{"rollback-job", "cloudProviders.addServer", "failed", "2/5", "Provisioning failed before rollback", "Original provisioning result", "Agent acknowledgement:"}},
		{"running empty job", `{"id":"deploy-job","type":"app.deploy","status":"running","progress":"2/5","currentStep":"build","teardowns":[],"teardownRead":{"aid":"a","cid":"c","jobId":"deploy-job"}}`, []string{"deploy-job", "app.deploy", "running", "2/5", "build"}},
		{"completed empty job", `{"id":"deploy-job","type":"app.deploy","status":"completed","teardowns":[],"teardownRead":{"aid":"a","cid":"c","jobId":"deploy-job"}}`, []string{"deploy-job", "app.deploy", "completed"}},
		{"immediate response", `{"success":true,"nid":"44444444-4444-4444-8444-444444444444","futureField":"Additional acceptance detail","teardown":` + teardownFixture + `}`, []string{"success", "true", "Additional acceptance detail", "Deletion accepted."}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := formatTeardownTest(t, test.body, "object")
			for _, want := range test.want {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q:\n%s", want, got)
				}
			}
			if strings.Contains(test.name, "empty job") && strings.Contains(got, "node-teardowns") {
				t.Errorf("unrelated job advertised teardown history:\n%s", got)
			}
		})
	}
}

func TestTeardownJobsListRendersMixedRows(t *testing.T) {
	body := `{"jobs":[
		{"id":"reset-job","type":"cluster.reset","status":"completed","teardowns":[` + teardownFixture + `],"teardownRead":{"aid":"a","cid":"c","jobId":"reset-job"},"teardownNextCursor":"next-page"},
		{"id":"deploy-job","type":"app.deploy","status":"completed","teardowns":[],"teardownRead":{"aid":"a","cid":"c","jobId":"deploy-job"}},
		{"id":"failed-read-job","type":"nodes.deleteAndReset","status":"running","teardowns":[],"teardownReadError":"Outcome storage unavailable","teardownRead":{"aid":"a","cid":"c","jobId":"failed-read-job"}}
	]}`
	got := captureStdout(t, func() {
		err := NewFormatter(false).Format([]byte(body), &manifest.Output{Type: "array", Fields: []manifest.OutputField{{Name: "id"}, {Name: "type"}, {Name: "status"}}})
		if err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"reset-job", "deploy-job", "app.deploy", "completed", "failed-read-job", "44444444-4444-4444-8444-444444444444", "Run runos uninstall locally when cleanup remains necessary.", "Outcome storage unavailable", "--job-id reset-job --cursor", "next-page", "--job-id failed-read-job"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "--job-id deploy-job") {
		t.Errorf("unrelated row advertised teardown history:\n%s", got)
	}
}

func TestTeardownHistoryContinuationDoesNotInferFilters(t *testing.T) {
	for _, first := range []string{teardownFixture, strings.Replace(teardownFixture, `"jobId": "55555555-5555-4555-8555-555555555555"`, `"jobId": null`, 1)} {
		second := strings.Replace(teardownFixture, `"jobId": "55555555-5555-4555-8555-555555555555"`, `"jobId": "66666666-6666-4666-8666-666666666666"`, 1)
		body := `{"teardowns":[` + first + `,` + second + `],"nextCursor":"opaque cursor/value"}`
		got := formatTeardownTest(t, body, "array")
		if strings.Contains(got, "--job-id") || strings.Contains(got, "runos node-teardowns list --cursor") {
			t.Errorf("response-only renderer invented a query:\n%s", got)
		}
		for _, want := range []string{"--cursor", "opaque cursor/value", "same history command", "filters and scope"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing pagination instruction %q:\n%s", want, got)
			}
		}
	}
}

func TestTeardownMalformedMetadataKeepsOriginalFields(t *testing.T) {
	for _, metadata := range []string{`"teardownRead":"unexpected read shape"`, `"teardownReadError":{"detail":"unexpected error shape"}`, `"teardowns":null`, `"teardownNextCursor":42`} {
		body := `{"id":"job","type":"app.deploy",` + metadata + `}`
		got := formatTeardownTest(t, body, "object")
		key := strings.SplitN(metadata, ":", 2)[0]
		if !strings.Contains(got, strings.Trim(key, `"`)) {
			t.Errorf("malformed metadata disappeared: %s", got)
		}
	}
}

func TestTeardownMissingRecordsAreNotSuccessfulEmptyHistory(t *testing.T) {
	body := `{"id":"job","type":"cluster.reset","status":"running","teardownRead":{"aid":"a","cid":"c","jobId":"job"}}`
	got := formatTeardownTest(t, body, "object")
	if strings.Contains(got, "No teardown records are available yet.") || !strings.Contains(got, "metadata is unavailable") {
		t.Fatalf("missing collection became an empty read: %s", got)
	}
}

func TestDestroyedProviderSuppressesOnlyLocalCleanup(t *testing.T) {
	for _, state := range []string{"pending", "acknowledged", "unacknowledged", "unknown", "not_requested"} {
		t.Run(state, func(t *testing.T) {
			var record map[string]any
			if err := json.Unmarshal([]byte(teardownFixture), &record); err != nil {
				t.Fatal(err)
			}
			record["state"] = state
			if state == "not_requested" {
				record["state"] = nil
				record["acknowledgementApplicable"] = false
			}
			record["providerState"] = "confirmed_destroyed"
			record["remedy"] = "Check the surviving machine. Kubernetes and cluster configuration can remain. Run runos uninstall locally when cleanup remains necessary. Wait for the unresolved dispatch guard before another operation."
			record["providerReason"] = "The provider confirmed server destruction."
			record["providerRemedy"] = "Verify the final invoice."
			body, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			got := formatTeardownTest(t, string(body), "object")
			for _, absent := range []string{"Check the surviving machine", "Run runos uninstall", "Kubernetes and cluster configuration can remain"} {
				if strings.Contains(got, absent) {
					t.Errorf("destroyed server has local cleanup advice %q:\n%s", absent, got)
				}
			}
			for _, want := range []string{"Wait for the unresolved dispatch guard", "The provider confirmed server destruction", "Verify the final invoice", "AGENT_UNREACHABLE", "Agent acknowledgement:"} {
				if !strings.Contains(got, want) {
					t.Errorf("missing independent evidence %q:\n%s", want, got)
				}
			}
			structured := captureStdout(t, func() {
				if err := NewFormatter(true).Format(body, &manifest.Output{Type: "object"}); err != nil {
					t.Fatal(err)
				}
			})
			var gotJSON map[string]any
			if err := json.Unmarshal([]byte(structured), &gotJSON); err != nil {
				t.Fatal(err)
			}
			if !equalJSONNumbers(record, gotJSON) {
				t.Fatalf("text suppression changed structured evidence: %s", structured)
			}
		})
	}
}

func TestUnrelatedJobMetadataDoesNotChangeText(t *testing.T) {
	legacy := `{"id":"job","type":"app.deploy","status":"completed","result":"Deployment is ready"}`
	current := strings.TrimSuffix(legacy, "}") + `,"teardowns":[],"teardownRead":{"aid":"a","cid":"c","jobId":"job"},"teardownReadError":null,"teardownNextCursor":null}`
	for _, kind := range []string{"object", "array"} {
		before, after := legacy, current
		if kind == "array" {
			before, after = `{"jobs":[`+before+`]}`, `{"jobs":[`+after+`]}`
		}
		want := formatTeardownTest(t, before, kind)
		if got := formatTeardownTest(t, after, kind); got != want {
			t.Errorf("unrelated %s output changed:\nwant %s\ngot %s", kind, want, got)
		}
	}
}

func formatTeardownTest(t *testing.T, body, kind string) string {
	t.Helper()
	return captureStdout(t, func() {
		if err := NewFormatter(false).Format([]byte(body), &manifest.Output{Type: kind}); err != nil {
			t.Fatal(err)
		}
	})
}
