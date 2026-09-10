package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const terminalHistoryCommand = "Read job outcomes: runos node-teardowns list --job-id 55555555-5555-4555-8555-555555555555 --cid 33333333-3333-4333-8333-333333333333\n"

func TestFollowDeltasEndWithUnchangedTeardownOutcome(t *testing.T) {
	t.Parallel()
	for _, acknowledgement := range []string{"pending", "unknown", "unacknowledged", "acknowledged"} {
		t.Run(acknowledgement, func(t *testing.T) {
			state := NewFollowState()
			for index, status := range []string{"running", "running", "completed"} {
				var job JobStatus
				if err := json.Unmarshal(terminalTeardownBody(status, acknowledgement), &job); err != nil {
					t.Fatal(err)
				}
				job.Progress = fmt.Sprintf("%d/3", index+1)
				items := []WorkItem{{ID: "step", Name: "cleanup", StepNumber: 1, Status: status}}
				var output bytes.Buffer
				EmitFollowDeltas(&output, &job, items, state)
				if index == 1 && strings.Contains(output.String(), "Agent acknowledgement:") {
					t.Fatalf("unchanged active outcome repeated: %s", output.String())
				}
				if status == "completed" {
					assertTerminalTeardown(t, output.String(), "step 1 cleanup: completed", acknowledgement)
				}
			}
		})
	}
}

func TestFollowLoopEndsWithTeardownAfterFinalLogs(t *testing.T) {
	t.Parallel()
	for _, terminal := range []string{"completed", "failed"} {
		t.Run(terminal, func(t *testing.T) {
			t.Parallel()
			var statusReads, itemReads, logReads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("unexpected method: %s", r.Method)
				}
				switch r.URL.Path {
				case "/jobs/job":
					status := "running"
					if statusReads.Add(1) >= 2 {
						status = terminal
					}
					_, _ = w.Write(terminalTeardownBody(status, "pending"))
				case "/jobs/job/workitems":
					status := "running"
					if itemReads.Add(1) >= 2 {
						status = terminal
					}
					fmt.Fprintf(w, `{"jobId":"job","workItems":[{"id":"step","name":"cleanup","status":%q,"stepNumber":1}],"hasMore":false}`, status)
				case "/jobs/job/workitems/step/logs":
					message := "Initial cleanup log"
					if logReads.Add(1) >= 2 {
						message = "Final cleanup log"
					}
					fmt.Fprintf(w, `{"workItemId":"step","logs":[{"id":"log","message":%q}],"nextCursor":null}`, message)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			service := &Service{baseURL: server.URL, httpClient: server.Client(), token: "test-token"}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var output bytes.Buffer
			final, err := FollowJobWithServiceToWriterResult(ctx, service, "job", &output)
			if (err != nil) != (terminal == "failed") {
				t.Fatalf("terminal %s returned %v", terminal, err)
			}
			if final == nil || !bytes.Equal(final.RawBody, terminalTeardownBody(terminal, "pending")) {
				t.Fatalf("terminal response changed: %#v", final)
			}
			if statusReads.Load() != 2 || itemReads.Load() != 2 || logReads.Load() != 2 {
				t.Fatalf("extra follow requests: status=%d items=%d logs=%d", statusReads.Load(), itemReads.Load(), logReads.Load())
			}
			assertTerminalTeardown(t, output.String(), "Final cleanup log", "pending")
			if got := strings.Count(output.String(), "Agent acknowledgement:"); got != 2 {
				t.Fatalf("outcome blocks = %d, want initial and terminal only", got)
			}
		})
	}
}

func terminalTeardownBody(status, acknowledgement string) []byte {
	body := string(acknowledgedTeardownJob(""))
	body = strings.Replace(body, `"status":"running",`, fmt.Sprintf(`"status":%q,"type":"cluster.reset","teardownRead":{"aid":"22222222-2222-4222-8222-222222222222","cid":"33333333-3333-4333-8333-333333333333","jobId":"55555555-5555-4555-8555-555555555555"},`, status), 1)
	body = strings.Replace(body, `"state":"acknowledged"`, fmt.Sprintf(`"state":%q`, acknowledgement), 1)
	return []byte(body)
}

func assertTerminalTeardown(t *testing.T, transcript, finalWork, acknowledgement string) {
	t.Helper()
	workIndex := strings.LastIndex(transcript, finalWork)
	outcomeIndex := strings.LastIndex(transcript, "Agent acknowledgement:")
	if workIndex < 0 || outcomeIndex < workIndex {
		t.Fatalf("terminal outcome did not follow final work:\n%s", transcript)
	}
	for _, want := range []string{"44444444-4444-4444-8444-444444444444", "Acknowledgement state: " + acknowledgement} {
		if !strings.Contains(transcript[outcomeIndex:], want) {
			t.Errorf("terminal outcome missing %q:\n%s", want, transcript)
		}
	}
	if !strings.HasSuffix(transcript, terminalHistoryCommand) {
		t.Errorf("transcript does not end with the later-read command:\n%s", transcript)
	}
}
