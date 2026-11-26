package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Each poller uses the real HTTP transport and its own job and progress writer.
func TestFollowTransportKeepsCallerProgressAndUncertainOutcome(t *testing.T) {
	for _, terminal := range []string{"completed", "failed", "cancelled"} {
		t.Run(terminal, func(t *testing.T) {
			t.Parallel()
			jobID := "job-" + terminal
			const uncertainty = "The script may still be running; it was not retried automatically."
			var reads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer fixture" {
					t.Error("unexpected request method or authentication")
				}
				switch r.URL.Path {
				case "/jobs/" + jobID:
					reads.Add(1)
					_ = json.NewEncoder(w).Encode(map[string]any{"id": jobID, "status": terminal, "error": uncertainty})
				case "/jobs/" + jobID + "/workitems":
					_ = json.NewEncoder(w).Encode(map[string]any{"workItems": []map[string]any{{"id": "step", "jobId": jobID, "name": "upgrade", "stepNumber": 1, "status": terminal}}})
				case "/jobs/" + jobID + "/workitems/step/logs":
					_ = json.NewEncoder(w).Encode(map[string]any{"logs": []map[string]string{{"id": "1", "message": jobID + ": first"}, {"id": "2", "message": jobID + ": second"}}})
				default:
					t.Errorf("wrong caller path %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			service := &Service{baseURL: server.URL, httpClient: server.Client(), token: "fixture"}
			var output bytes.Buffer
			final, err := FollowJobWithServiceToWriterResult(context.Background(), service, jobID, &output)
			if final == nil || final.Status != terminal || reads.Load() != 1 {
				t.Fatalf("terminal was not final: %+v reads=%d", final, reads.Load())
			}
			if terminal == "completed" && err != nil {
				t.Fatal(err)
			}
			if terminal != "completed" && (err == nil || !strings.Contains(err.Error(), uncertainty)) {
				t.Fatalf("lost uncertain outcome: %v", err)
			}
			text := output.String()
			first, second := strings.Index(text, jobID+": first"), strings.Index(text, jobID+": second")
			if first < 0 || second <= first {
				t.Fatalf("progress lost or reordered: %q", text)
			}
			for _, other := range []string{"completed", "failed", "cancelled"} {
				if other != terminal && strings.Contains(text, "job-"+other) {
					t.Fatalf("another caller's progress: %q", text)
				}
			}
		})
	}
}
