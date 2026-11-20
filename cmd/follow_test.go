package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/spf13/cobra"
)

// newFollowTestCmd builds a cobra.Command with the same flag shape as
// the real followCmd, without reusing the shared package-level
// variable (avoids cross-test flag-state leakage).
func newFollowTestCmd() *cobra.Command {
	c := &cobra.Command{Use: "follow"}
	c.Flags().BoolP("json", "j", false, "")
	return c
}

// TestRunFollow_JSONKeySet is the cmd-level test finding 1409 asks
// for: `runos follow --json`'s document carries the same key set
// (id/type/status/progress/currentStep/error) whether the sanitized
// progress endpoint served the follow (a deploy job) or the general
// route did (a non-deploy job) -- proving story 346 finding 1351's
// fix actually holds the published --json contract stable across both
// sources, not only that the code compiles.
func TestRunFollow_JSONKeySet(t *testing.T) {
	const jobID = "55555555-5555-4555-8555-555555555555"

	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "deploy job, served by the sanitized progress endpoint",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/jobs/"+jobID+"/deployment-progress" {
					w.Write([]byte(`{"version":"runos.deployment-progress.v1","jobId":"` + jobID + `","appId":"app01","state":"completed"}`))
					return
				}
				http.NotFound(w, r)
			},
		},
		{
			name: "non-deploy job, served by the general route",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/jobs/" + jobID + "/deployment-progress":
					http.NotFound(w, r)
				case "/jobs/" + jobID:
					w.Write([]byte(`{"id":"` + jobID + `","type":"app.deploy","status":"completed","progress":"done","currentStep":"finish"}`))
				case "/jobs/" + jobID + "/workitems":
					w.Write([]byte(`{"jobId":"` + jobID + `","workItems":[]}`))
				default:
					http.NotFound(w, r)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(tc.handler))
			t.Cleanup(srv.Close)

			t.Setenv("HOME", t.TempDir())
			t.Setenv("RUNOS_API_KEY", "test-key")
			t.Setenv("RUNOS_API_URL", srv.URL)

			c := newFollowTestCmd()
			if err := c.Flags().Set("json", "true"); err != nil {
				t.Fatalf("set json flag: %v", err)
			}

			origStdout := os.Stdout
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatalf("pipe: %v", err)
			}
			os.Stdout = w
			runErr := runFollow(c, []string{jobID})
			w.Close()
			os.Stdout = origStdout
			if runErr != nil {
				t.Fatalf("runFollow: %v", runErr)
			}

			var captured bytes.Buffer
			if _, err := captured.ReadFrom(r); err != nil {
				t.Fatalf("read captured stdout: %v", err)
			}

			var doc map[string]any
			if err := json.Unmarshal(captured.Bytes(), &doc); err != nil {
				t.Fatalf("stdout was not one JSON document: %v\n%s", err, captured.String())
			}
			for _, key := range []string{"id", "type", "status", "progress", "currentStep"} {
				if _, ok := doc[key]; !ok {
					t.Errorf("document missing key %q: %v", key, doc)
				}
			}
			if doc["id"] != jobID {
				t.Errorf("id = %v, want %q", doc["id"], jobID)
			}
			if doc["status"] != "completed" {
				t.Errorf("status = %v, want %q", doc["status"], "completed")
			}
		})
	}
}
