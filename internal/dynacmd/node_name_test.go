package dynacmd

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/manifest"
	"github.com/spf13/cobra"
)

// Every value in this file is a placeholder. No real account id, cluster
// id, node id, node name or hostname belongs in the repository.
const (
	testAccountID = "acct1"
	testClusterID = "cluster1"
	testNodeID    = "node-1"
	testPAT       = "pat-test-token"
)

// nodesDelete and nodesDrain are the only two gated commands that display
// the node id field as their target.
var nodesDelete = manifest.Command{
	Command: "nodes/{nid}/delete",
	Method:  "DELETE",
	Input: &manifest.Input{Fields: []manifest.Field{
		{Name: "nid", Type: "string", Positional: true, Required: true},
	}},
}

var nodesDrain = manifest.Command{
	Command: "nodes/{nid}/drain",
	Method:  "POST",
	Input: &manifest.Input{Fields: []manifest.Field{
		{Name: "nid", Type: "string", Positional: true, Required: true},
	}},
}

// localhostURL rewrites an httptest address to the localhost form.
// config.GetAPIURL prints a scheme warning for any non-HTTPS URL except a
// localhost one, and a test that asserts an empty stderr must not trip a
// warning that belongs to the config package.
func localhostURL(raw string) string {
	return strings.Replace(raw, "127.0.0.1", "localhost", 1)
}

// stubNodeRead answers the single node read with the given status and body.
func stubNodeRead(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// useNodeNameConfig injects the config the lookup reads and clears every
// environment variable the config getters prefer, so no test depends on a
// developer's config file or shell.
func useNodeNameConfig(t *testing.T, cfg *config.Config) {
	t.Helper()
	clearNodeNameEnv(t)
	previous := loadNodeNameConfig
	loadNodeNameConfig = func() (*config.Config, error) { return cfg, nil }
	t.Cleanup(func() { loadNodeNameConfig = previous })
}

// clearNodeNameEnv empties the environment variables the config getters
// prefer over the injected config.
func clearNodeNameEnv(t *testing.T) {
	t.Helper()
	t.Setenv("RUNOS_API_KEY", "")
	t.Setenv("RUNOS_ACCOUNT_ID", "")
	t.Setenv("RUNOS_CLUSTER_ID", "")
	t.Setenv("RUNOS_API_URL", "")
}

// patConfig is a config whose credential resolves locally, with no token
// round trip, so a test of the API branch measures only the API branch.
func patConfig(apiURL string) *config.Config {
	return &config.Config{
		AccountID:        testAccountID,
		DefaultClusterID: testClusterID,
		APIKey:           testPAT,
		ConductorURL:     apiURL,
	}
}

// interactiveConfig is a config whose credential needs a token refresh,
// which is the path that carries its own ten second client.
func interactiveConfig(apiURL string) *config.Config {
	return &config.Config{
		AccountID:        testAccountID,
		DefaultClusterID: testClusterID,
		RefreshToken:     "refresh-token-placeholder",
		Firebase:         &config.FirebaseConfig{APIKey: "firebase-key-placeholder"},
		ConductorURL:     apiURL,
	}
}

// waitForNodeNameWorker installs the worker completion hook and returns
// the wait function a deadline test must call before the test returns.
//
// A deadline test abandons a worker that still reads the package level
// seams, and every seam restore runs in a t.Cleanup. Waiting for the
// worker first is what keeps the restore and the worker from being an
// unsynchronised pair the race detector reports.
func waitForNodeNameWorker(t *testing.T) func() {
	t.Helper()
	stopped := make(chan struct{})
	previous := nodeNameWorkerDone
	nodeNameWorkerDone = func() { close(stopped) }
	t.Cleanup(func() { nodeNameWorkerDone = previous })
	return func() {
		t.Helper()
		select {
		case <-stopped:
		case <-time.After(30 * time.Second):
			t.Fatal("the abandoned node name worker never stopped, so a seam restore can race it")
		}
	}
}

// captureStderr records everything the callback writes to stderr. The
// lookup must write nothing at all, whatever it fails on.
func captureStderr(t *testing.T, run func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	previous := os.Stderr
	os.Stderr = writer
	run()
	os.Stderr = previous
	if err := writer.Close(); err != nil {
		t.Fatalf("close the pipe: %v", err)
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read the pipe: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close the reader: %v", err)
	}
	return string(out)
}

// The defect this story fixes: `nodes delete <node-id>` printed the node
// id alone, so the operator could not tell which machine that node id was.
// The node id stays on the line, because the operator types the node id
// and can mistype it.
func TestDestructiveSummary_NamesTheNode(t *testing.T) {
	t.Run("nodes delete shows the node id and the node name", func(t *testing.T) {
		srv := stubNodeRead(t, http.StatusOK, `{"nid":"node-1","name":"node-alpha","hostname":"host-alpha"}`)
		useNodeNameConfig(t, patConfig(localhostURL(srv.URL)))

		got := destructiveSummary(nil, nodesDelete, []string{testNodeID})
		if want := "nid=node-1 name=node-alpha"; got != want {
			t.Errorf("target = %q, want %q", got, want)
		}
	})

	t.Run("nodes drain shows the same shape", func(t *testing.T) {
		srv := stubNodeRead(t, http.StatusOK, `{"nid":"node-1","name":"node-alpha"}`)
		useNodeNameConfig(t, patConfig(localhostURL(srv.URL)))

		got := destructiveSummary(nil, nodesDrain, []string{testNodeID})
		if want := "nid=node-1 name=node-alpha"; got != want {
			t.Errorf("target = %q, want %q", got, want)
		}
	})

	// `clusters/{cid}/reset` proved that some commands carry the URL-bound
	// id only as a flag (#131). The flag form of the node id decorates too.
	t.Run("the node id passed as a flag decorates the same way", func(t *testing.T) {
		srv := stubNodeRead(t, http.StatusOK, `{"name":"node-alpha"}`)
		useNodeNameConfig(t, patConfig(localhostURL(srv.URL)))

		c := &cobra.Command{Use: "x"}
		c.Flags().String("nid", "", "")
		if err := c.Flags().Set("nid", testNodeID); err != nil {
			t.Fatalf("seed the flag: %v", err)
		}
		got := destructiveSummary(c, nodesDelete, nil)
		if want := "nid=node-1 name=node-alpha"; got != want {
			t.Errorf("target = %q, want %q", got, want)
		}
	})

	// The lookup addresses the cluster the executor would address: the
	// --cid flag first, then a cid positional, then the default cluster.
	t.Run("the cid flag wins over the default cluster", func(t *testing.T) {
		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			fmt.Fprint(w, `{"name":"node-alpha"}`)
		}))
		t.Cleanup(srv.Close)
		useNodeNameConfig(t, patConfig(localhostURL(srv.URL)))

		c := &cobra.Command{Use: "x"}
		c.Flags().String("cid", "", "")
		if err := c.Flags().Set("cid", "cluster2"); err != nil {
			t.Fatalf("seed the flag: %v", err)
		}
		if got := destructiveSummary(c, nodesDelete, []string{testNodeID}); got != "nid=node-1 name=node-alpha" {
			t.Errorf("target = %q", got)
		}
		if want := "/acct1/cluster2/nodes/node-1"; gotPath != want {
			t.Errorf("path = %q, want %q", gotPath, want)
		}
	})
}

// A failure to resolve the node name never blocks the command, never
// prints a warning, and never becomes an error. Every one of these prints
// the node id alone.
func TestDestructiveSummary_NodeNameFallbacks(t *testing.T) {
	reachable := stubNodeRead(t, http.StatusOK, `{"name":"node-alpha"}`)
	// A closed server keeps its address, so a request to it fails at once.
	closed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	closedURL := localhostURL(closed.URL)
	closed.Close()

	cases := []struct {
		name string
		cfg  *config.Config
	}{
		{"no config account id", &config.Config{DefaultClusterID: testClusterID, APIKey: testPAT, ConductorURL: localhostURL(reachable.URL)}},
		{"no cluster id from any source", &config.Config{AccountID: testAccountID, APIKey: testPAT, ConductorURL: localhostURL(reachable.URL)}},
		{"no credential on the machine", &config.Config{AccountID: testAccountID, DefaultClusterID: testClusterID, ConductorURL: localhostURL(reachable.URL)}},
		{"the request fails", patConfig(closedURL)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useNodeNameConfig(t, tc.cfg)
			var got string
			stderr := captureStderr(t, func() {
				got = destructiveSummary(nil, nodesDelete, []string{testNodeID})
			})
			if want := "nid=node-1"; got != want {
				t.Errorf("target = %q, want %q", got, want)
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want nothing", stderr)
			}
		})
	}

	// An unknown node id answers 404, and a node id that fails conductor's
	// shape guard answers 400. The code branches on "not a success", never
	// on one status code.
	t.Run("a result that is not a success", func(t *testing.T) {
		for _, status := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusUnauthorized, http.StatusInternalServerError} {
			srv := stubNodeRead(t, status, `{"error":"Node not found"}`)
			useNodeNameConfig(t, patConfig(localhostURL(srv.URL)))
			var got string
			stderr := captureStderr(t, func() {
				got = destructiveSummary(nil, nodesDelete, []string{testNodeID})
			})
			if want := "nid=node-1"; got != want {
				t.Errorf("HTTP %d target = %q, want %q", status, got, want)
			}
			if stderr != "" {
				t.Errorf("HTTP %d stderr = %q, want nothing", status, stderr)
			}
		}
	})

	t.Run("the token does not resolve", func(t *testing.T) {
		token := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"TOKEN_EXPIRED"}}`)
		}))
		t.Cleanup(token.Close)
		auth.SetEndpointsForTest(t, token.URL, token.URL)
		srv := stubNodeRead(t, http.StatusOK, `{"name":"node-alpha"}`)
		useNodeNameConfig(t, interactiveConfig(localhostURL(srv.URL)))

		var got string
		stderr := captureStderr(t, func() {
			got = destructiveSummary(nil, nodesDelete, []string{testNodeID})
		})
		if want := "nid=node-1"; got != want {
			t.Errorf("target = %q, want %q", got, want)
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want nothing", stderr)
		}
	})

	t.Run("the config does not load", func(t *testing.T) {
		clearNodeNameEnv(t)
		previous := loadNodeNameConfig
		loadNodeNameConfig = func() (*config.Config, error) { return nil, fmt.Errorf("config not found") }
		t.Cleanup(func() { loadNodeNameConfig = previous })

		if got, want := destructiveSummary(nil, nodesDelete, []string{testNodeID}), "nid=node-1"; got != want {
			t.Errorf("target = %q, want %q", got, want)
		}
	})
}

// The label rule. The node rename route refuses a control character, but
// the two node registration routes validate nothing, so a stored node name
// can carry surrounding space, only space characters, or a control
// character. The prompt is the one line the operator reads before an
// irreversible command, so a node name that can rewrite the line is
// rejected and the prompt prints the node id alone.
func TestDestructiveSummary_NodeLabelRule(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"a name reaches the line", `{"name":"node-alpha"}`, "nid=node-1 name=node-alpha"},
		{"surrounding space is trimmed", `{"name":"  node-alpha  "}`, "nid=node-1 name=node-alpha"},
		{"a node with no assigned name prints the node id alone", `{"name":""}`, "nid=node-1"},
		{"only space characters prints the node id alone", `{"name":"   "}`, "nid=node-1"},
		// The JSON escape below decodes to a real escape character, so the
		// case proves that no escape sequence can reach the target line.
		{"an escape character prints the node id alone", `{"name":"\u001b[2Knode-beta"}`, "nid=node-1"},
		{"a newline prints the node id alone", `{"name":"node-alpha\nProceed? [y/N] y"}`, "nid=node-1"},
		{"a name field the record does not carry prints the node id alone", `{"nid":"node-1"}`, "nid=node-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := stubNodeRead(t, http.StatusOK, tc.body)
			useNodeNameConfig(t, patConfig(localhostURL(srv.URL)))

			got := destructiveSummary(nil, nodesDelete, []string{testNodeID})
			if got != tc.want {
				t.Errorf("target = %q, want %q", got, tc.want)
			}
			if strings.ContainsAny(got, "\x1b\n\r") {
				t.Errorf("target = %q, want no control character in the line", got)
			}
		})
	}
}

// The deadline covers the WHOLE decoration, not the API call alone. Three
// steps can be slow, and each one carries its own ten second client or its
// own remote fetch, so each step needs a test of its own.
//
// Every test here follows the same four rules: the slow step blocks on a
// channel the test owns and never sleeps; the worker signals when it
// stops; the test asserts the timing, unblocks the step, then waits for
// the worker before returning; and no stub touches the test handle.
func TestDestructiveSummary_NodeNameDeadline(t *testing.T) {
	// The margin allows for a loaded machine. The point of each assertion
	// is that the caller returns in about the deadline rather than in the
	// ten seconds the slow step would otherwise take.
	const margin = 4 * time.Second

	assertUndecoratedAndQuick := func(t *testing.T, elapsed time.Duration, got string) {
		t.Helper()
		if want := "nid=node-1"; got != want {
			t.Errorf("target = %q, want %q", got, want)
		}
		if elapsed < nodeNameDeadline {
			t.Errorf("returned in %v, want at least the deadline %v", elapsed, nodeNameDeadline)
		}
		if elapsed > nodeNameDeadline+margin {
			t.Errorf("returned in %v, want about the deadline %v", elapsed, nodeNameDeadline)
		}
	}

	t.Run("over the config load", func(t *testing.T) {
		release := make(chan struct{})
		clearNodeNameEnv(t)
		previousLoader := loadNodeNameConfig
		loadNodeNameConfig = func() (*config.Config, error) {
			<-release
			return nil, fmt.Errorf("config not found")
		}
		t.Cleanup(func() { loadNodeNameConfig = previousLoader })
		waitForWorker := waitForNodeNameWorker(t)

		start := time.Now()
		got := destructiveSummary(nil, nodesDelete, []string{testNodeID})
		assertUndecoratedAndQuick(t, time.Since(start), got)

		close(release)
		waitForWorker()
	})

	t.Run("over the token resolution", func(t *testing.T) {
		release := make(chan struct{})
		token := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			<-release
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"TOKEN_EXPIRED"}}`)
		}))
		t.Cleanup(token.Close)
		auth.SetEndpointsForTest(t, token.URL, token.URL)
		node := stubNodeRead(t, http.StatusOK, `{"name":"node-alpha"}`)
		useNodeNameConfig(t, interactiveConfig(localhostURL(node.URL)))
		waitForWorker := waitForNodeNameWorker(t)

		start := time.Now()
		got := destructiveSummary(nil, nodesDelete, []string{testNodeID})
		assertUndecoratedAndQuick(t, time.Since(start), got)

		close(release)
		waitForWorker()
	})

	t.Run("over the node read", func(t *testing.T) {
		release := make(chan struct{})
		node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			<-release
			fmt.Fprint(w, `{"name":"node-alpha"}`)
		}))
		t.Cleanup(node.Close)
		useNodeNameConfig(t, patConfig(localhostURL(node.URL)))
		waitForWorker := waitForNodeNameWorker(t)

		start := time.Now()
		got := destructiveSummary(nil, nodesDelete, []string{testNodeID})
		assertUndecoratedAndQuick(t, time.Since(start), got)

		close(release)
		waitForWorker()
	})
}

// nodeLabel is the one place that decides what the prompt may print, so
// the console and the CLI can hold the same rule. The hostname is never a
// fallback.
func TestNodeLabel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"node-alpha", "node-alpha"},
		{"  node-alpha  ", "node-alpha"},
		{"node alpha two", "node alpha two"},
		{"", ""},
		{"   ", ""},
		{"\t\n", ""},
		{"node\x1b[2Kalpha", ""},
		{"node\nalpha", ""},
		{"node\x00alpha", ""},
	}
	for _, tc := range cases {
		if got := nodeLabel(tc.in); got != tc.want {
			t.Errorf("nodeLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
