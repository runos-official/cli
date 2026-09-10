package output

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

const teardownFixture = `{
  "id": "11111111-1111-4111-8111-111111111111",
  "aid": "22222222-2222-4222-8222-222222222222",
  "cid": "33333333-3333-4333-8333-333333333333",
  "nid": "44444444-4444-4444-8444-444444444444",
  "name": "  target node  ",
  "jobId": "55555555-5555-4555-8555-555555555555",
  "operationKind": "cluster_reset",
  "acknowledgementApplicable": true,
  "state": "unacknowledged",
  "acceptanceState": "accepted",
  "reasonCode": "AGENT_UNREACHABLE",
  "reason": "The agent did not answer the uninstall request. This reason is deliberately longer than eighty characters and stays fully readable.",
  "remedy": "Check the surviving machine. Kubernetes and cluster configuration can remain. Run runos uninstall locally when cleanup remains necessary.",
  "requestedAt": "2026-09-09T12:00:00.123Z",
  "dispatchedAt": "2026-09-09T12:00:01Z",
  "resolvedAt": "2026-09-09T12:00:31Z",
  "updatedAt": "2026-09-09T12:00:31Z",
  "trackingDeadlineAt": "2026-09-09T12:01:01Z",
  "providerState": "failed",
  "providerReason": "The provider did not confirm destruction.",
  "providerRemedy": "Check the provider account because billing can continue.",
  "futureNumber": 12.5
}`

func TestFormatterRendersCompleteTeardownHistory(t *testing.T) {
	body := []byte(`{"teardowns":[` + teardownFixture + `],"nextCursor":"opaque cursor/value"}`)
	outputDef := &manifest.Output{
		Type: "array",
		Fields: []manifest.OutputField{
			{Name: "id"},
			{Name: "nid"},
			{Name: "state"},
			{Name: "reason"},
			{Name: "remedy"},
		},
	}

	rendered := captureStdout(t, func() {
		if err := NewFormatter(false).Format(body, outputDef); err != nil {
			t.Fatalf("format teardown history: %v", err)
		}
	})

	for _, expected := range []string{
		"target node",
		"44444444-4444-4444-8444-444444444444",
		"Agent acknowledgement: Positive acknowledgement was not obtained.",
		"The agent did not answer the uninstall request. This reason is deliberately longer than eighty characters and stays fully readable.",
		"Run runos uninstall locally when cleanup remains necessary.",
		"Provider destruction: Failed.",
		"Continue the same history command with --cursor 'opaque cursor/value'; keep all existing filters and scope.",
	} {
		if !strings.Contains(rendered, expected) {
			t.Errorf("teardown output missing %q:\n%s", expected, rendered)
		}
	}
}

func TestFormatterRendersImmediateTeardownWithoutCompletionClaim(t *testing.T) {
	body := []byte(`{"success":true,"nid":"44444444-4444-4444-8444-444444444444","teardown":` + teardownFixture + `}`)
	rendered := captureStdout(t, func() {
		if err := NewFormatter(false).Format(body, &manifest.Output{Type: "object"}); err != nil {
			t.Fatalf("format immediate teardown: %v", err)
		}
	})

	if strings.Contains(rendered, "uninstall completed") || strings.Contains(rendered, "reboot completed") {
		t.Fatalf("immediate output claimed machine completion:\n%s", rendered)
	}
	for _, expected := range []string{
		"Deletion accepted.",
		"runos node-teardowns show 11111111-1111-4111-8111-111111111111 --cid 33333333-3333-4333-8333-333333333333",
	} {
		if !strings.Contains(rendered, expected) {
			t.Errorf("immediate output missing %q:\n%s", expected, rendered)
		}
	}
}

func TestFormatterJSONPreservesTeardownResponse(t *testing.T) {
	body := []byte(`{"teardowns":[` + teardownFixture + `],"nextCursor":null,"unknown":{"count":9007199254740993}}`)
	rendered := captureStdout(t, func() {
		if err := NewFormatter(true).Format(body, &manifest.Output{Type: "array"}); err != nil {
			t.Fatalf("format JSON: %v", err)
		}
	})

	var want any
	var got any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&want); err != nil {
		t.Fatal(err)
	}
	decoder = json.NewDecoder(strings.NewReader(rendered))
	decoder.UseNumber()
	if err := decoder.Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !equalJSONNumbers(want, got) {
		t.Fatalf("structured output changed:\nwant %s\n got %s", body, rendered)
	}
}

func TestTeardownAcknowledgementStateMessages(t *testing.T) {
	tests := []struct {
		name       string
		applicable bool
		state      string
		expected   string
	}{
		{name: "pending", applicable: true, state: "pending", expected: "Scheduling acknowledgement remains pending."},
		{name: "acknowledged", applicable: true, state: "acknowledged", expected: "acknowledged scheduling uninstall"},
		{name: "unacknowledged", applicable: true, state: "unacknowledged", expected: "Positive acknowledgement was not obtained."},
		{name: "unknown", applicable: true, state: "unknown", expected: "Tracking cannot establish the agent outcome."},
		{name: "not requested", applicable: false, expected: "Uninstall acknowledgement was not requested."},
		{name: "future value", applicable: true, state: "future_state", expected: "Unknown state: future_state."},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := map[string]any{
				"acknowledgementApplicable": test.applicable,
				"state":                     test.state,
			}
			if got := acknowledgementMessage(record); !strings.Contains(got, test.expected) {
				t.Fatalf("acknowledgementMessage() = %q, want text %q", got, test.expected)
			}
		})
	}
}

func TestFormatterTeardownFallbacksAndLimitations(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		expected   []string
		unexpected []string
	}{
		{
			name: "control name falls back to full id",
			body: strings.Replace(teardownFixture, `"  target node  "`, `"unsafe\nname"`, 1),
			expected: []string{
				"Node id: 44444444-4444-4444-8444-444444444444",
			},
			unexpected: []string{"Node: unsafe"},
		},
		{
			name: "read failure is not empty history",
			body: `{"teardowns":[],"teardownReadError":"The result store is unavailable.","teardownRead":{"aid":"a","cid":"c","jobId":"j"}}`,
			expected: []string{
				"Teardown outcomes are unavailable: The result store is unavailable.",
				"runos node-teardowns list --job-id j --cid c",
			},
			unexpected: []string{"No teardown records are available yet."},
		},
		{
			name:       "older response stays legacy",
			body:       `{"id":"legacy-job","status":"completed","teardownRead":null,"teardownReadError":null}`,
			expected:   []string{"id", "legacy-job", "status", "completed"},
			unexpected: []string{"acknowledgement", "uninstall", "No teardown records"},
		},
		{
			name: "malformed metadata stays visible",
			body: `{"teardowns":[{"id":"partial"}],"nextCursor":"cursor"}`,
			expected: []string{
				"teardowns",
				"partial",
				"nextCursor",
			},
			unexpected: []string{"No teardown records are available yet."},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rendered := captureStdout(t, func() {
				if err := NewFormatter(false).Format([]byte(test.body), &manifest.Output{Type: "object"}); err != nil {
					t.Fatal(err)
				}
			})
			for _, expected := range test.expected {
				if !strings.Contains(rendered, expected) {
					t.Errorf("output missing %q:\n%s", expected, rendered)
				}
			}
			for _, unexpected := range test.unexpected {
				if strings.Contains(strings.ToLower(rendered), strings.ToLower(unexpected)) {
					t.Errorf("output unexpectedly contains %q:\n%s", unexpected, rendered)
				}
			}
		})
	}
}

func TestFormatterQueuedJobShowsReadCommand(t *testing.T) {
	body := []byte(`{"jobId":"55555555-5555-4555-8555-555555555555"}`)
	rendered := captureStdout(t, func() {
		if err := NewFormatter(false).Format(body, &manifest.Output{Type: "object"}); err != nil {
			t.Fatal(err)
		}
	})
	for _, expected := range []string{
		"jobId",
		"55555555-5555-4555-8555-555555555555",
		"Work accepted.",
		"runos jobs show 55555555-5555-4555-8555-555555555555",
	} {
		if !strings.Contains(rendered, expected) {
			t.Errorf("queued job output missing %q:\n%s", expected, rendered)
		}
	}
}

func TestFormatterJobReadDoesNotClaimNewAcceptance(t *testing.T) {
	body := []byte(`{"jobId":"55555555-5555-4555-8555-555555555555","workItems":[]}`)
	rendered := captureStdout(t, func() {
		if err := NewFormatter(false).Format(body, &manifest.Output{Type: "object"}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(rendered, "Work accepted.") {
		t.Fatalf("job read claimed a new accepted operation:\n%s", rendered)
	}
}

func equalJSONNumbers(want, got any) bool {
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got)
	return string(wantJSON) == string(gotJSON)
}
