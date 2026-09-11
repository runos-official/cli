package dynacmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/manifest"
	"github.com/spf13/cobra"
)

var evictionCommand = manifest.Command{
	Command: "storage-groups/evict-node",
	Method:  "POST",
	Input: &manifest.Input{Fields: []manifest.Field{
		{Name: "nid", Type: "string"},
		{Name: "hostname", Type: "string"},
		{Name: "acknowledgeDataLoss", Type: "boolean"},
	}},
}

func TestEvictionTargetReadIsNotDestructive(t *testing.T) {
	read := manifest.Command{Command: "storage-groups/evict-node-target", Method: "GET"}
	if IsDestructiveCommand(read) {
		t.Fatal("advisory target GET must not require destructive confirmation")
	}
	if !IsDestructiveCommand(evictionCommand) {
		t.Fatal("eviction POST must still require confirmation")
	}
}

func TestEvictionHostnameNamesCurrentNode(t *testing.T) {
	srv := stubNodeRead(t, 200, `{"outcome":"matched","hostname":"host-new","nid":null,"runosNodeName":" worker one ","runosNid":"node-full-0123456789","source":"nodes","candidateNids":[],"unavailableSources":[]}`)
	useNodeNameConfig(t, patConfig(localhostURL(srv.URL)))
	c := seedFlags(t, map[string]string{"hostname": "host-new"})
	want := "hostname=host-new nid=node-full-0123456789 name=worker one"
	if got := destructiveSummary(c, evictionCommand, nil); got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

const fullEvictionID = "node-full-0123456789"
const cleanupSuffix = " (device-record cleanup nid=" + fullEvictionID + "; only matching records for this hostname would be removed)"

type evictionCase struct {
	name, outcome string
	patch         map[string]any
	want          string
	status        int
}

func evictionCases() []evictionCase {
	return []evictionCase{
		{"node only with OLD device rows outside this target", "matched", nil, " nid=" + fullEvictionID + " name=worker one", 200},
		{"NEW device rows alongside OLD rows", "matched", map[string]any{"nid": fullEvictionID, "source": "devices"}, " nid=" + fullEvictionID + " name=worker one" + cleanupSuffix, 200},
		{"unnamed", "matched", map[string]any{"runosNodeName": ""}, " nid=" + fullEvictionID, 200},
		{"whitespace name", "matched", map[string]any{"runosNodeName": " \t "}, " nid=" + fullEvictionID, 200},
		{"control name", "matched", map[string]any{"runosNodeName": "bad\x1b[2Jname"}, " nid=" + fullEvictionID, 200},
		{"complete absence", "no_record", map[string]any{"runosNid": "", "runosNodeName": "", "source": nil}, " (RunOS has no record of this machine)", 200},
		{"device remnant", "no_record", map[string]any{"nid": fullEvictionID, "runosNid": "", "runosNodeName": "", "source": "devices"}, " (no current RunOS node record; device records remain)" + cleanupSuffix, 200},
		{"unknown with cleanup", "unknown", map[string]any{"nid": fullEvictionID, "runosNid": "", "runosNodeName": "", "source": "devices", "unavailableSources": []string{"nodes"}}, " (current node identity could not be established)" + cleanupSuffix, 200},
		{"unknown without facts", "unknown", map[string]any{"runosNid": "", "runosNodeName": "", "source": nil, "unavailableSources": []string{"devices", "nodes"}}, " (current node identity could not be established)", 200},
		{"unknown with partial current identity", "unknown", map[string]any{"unavailableSources": []string{"devices"}}, " (current node identity could not be established)", 200},
		{"ambiguous", "ambiguous", map[string]any{"runosNid": "", "runosNodeName": "", "source": nil, "candidateNids": []string{fullEvictionID, "node-full-9876543210"}}, " (ambiguous hostname; CLI refuses to name one node)", 200},
		{"unexpected missing hostname", "missing_hostname", map[string]any{"hostname": nil}, evictionLookupFailed, 200},
		{"malformed response", "future", nil, evictionLookupFailed, 200},
		{"http failure", "unknown", nil, evictionLookupFailed, 503},
		{"connection closed", "unknown", nil, evictionLookupFailed, 0},
	}
}

func evictionLeaf(t *testing.T) *cobra.Command {
	t.Helper()
	// A nil executor makes accidental progress beyond refusal fail the test.
	c := (&Builder{}).buildLeafCommand("evict-node", evictionCommand)
	if err := c.ParseFlags([]string{"--hostname", "host-new", "--acknowledge-data-loss"}); err != nil {
		t.Fatal(err)
	}
	return c
}

func useEvictionCase(t *testing.T, tc evictionCase) *atomic.Int32 {
	t.Helper()
	var requests atomic.Int32
	payload := map[string]any{
		"outcome": tc.outcome, "hostname": "host-new", "nid": nil,
		"runosNodeName": " worker one ", "runosNid": fullEvictionID,
		"source": "nodes", "candidateNids": []string{}, "unavailableSources": []string{},
	}
	for key, value := range tc.patch {
		payload[key] = value
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "GET" || r.URL.Path != "/acct1/cluster1/storage-groups/evict-node-target" || r.URL.Query().Get("hostname") != "host-new" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		if tc.status == 0 {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			connection.Close()
			return
		}
		w.WriteHeader(tc.status)
		json.NewEncoder(w).Encode(payload)
	}))
	t.Cleanup(srv.Close)
	useNodeNameConfig(t, patConfig(localhostURL(srv.URL)))
	return &requests
}

func TestEvictionHostnameNonInteractiveMatrix(t *testing.T) {
	for _, tc := range evictionCases() {
		t.Run(tc.name, func(t *testing.T) {
			requests := useEvictionCase(t, tc)
			c := evictionLeaf(t)
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			writer.Close()
			previous := os.Stdin
			os.Stdin = reader
			defer func() { os.Stdin = previous }()
			var refusal error
			stderr := captureStderr(t, func() { refusal = c.RunE(c, nil) })
			want := "storage-groups evict-node is destructive and requires confirmation. Re-run with --yes to proceed (target: hostname=host-new" + tc.want + " acknowledge-data-loss=true)"
			if refusal == nil || refusal.Error() != want {
				t.Fatalf("refusal=%v, want=%q", refusal, want)
			}
			if stderr != "" || requests.Load() != 1 {
				t.Fatalf("stderr=%q; reads=%d", stderr, requests.Load())
			}
		})
	}
}

func TestEvictionHostnameLookupInvariants(t *testing.T) {
	t.Run("API URL warning does not escape the decoration", func(t *testing.T) {
		srv := stubNodeRead(t, 503, `{}`)
		useNodeNameConfig(t, patConfig(srv.URL))
		c := seedFlags(t, map[string]string{"hostname": "host-new"})
		stderr := captureStderr(t, func() { destructiveSummary(c, evictionCommand, nil) })
		if stderr != "" {
			t.Fatalf("decoration wrote a warning: %q", stderr)
		}
	})
	t.Run("node ID line remains byte identical", func(t *testing.T) {
		srv := stubNodeRead(t, 200, `{"name":" worker one "}`)
		useNodeNameConfig(t, patConfig(localhostURL(srv.URL)))
		c := seedFlags(t, map[string]string{"nid": fullEvictionID})
		if got := destructiveSummary(c, evictionCommand, nil); got != "nid="+fullEvictionID+" name=worker one" {
			t.Fatal(got)
		}
	})
	t.Run("unrelated hostname is not decorated", func(t *testing.T) {
		requests := useEvictionCase(t, evictionCases()[0])
		other := evictionCommand
		other.Command = "other/evict-node"
		c := seedFlags(t, map[string]string{"hostname": "host-new"})
		if got := destructiveSummary(c, other, nil); got != "hostname=host-new" || requests.Load() != 0 {
			t.Fatalf("summary=%s; reads=%d", got, requests.Load())
		}
	})
	t.Run("duplicate manifest fields perform one read", func(t *testing.T) {
		requests := useEvictionCase(t, evictionCases()[0])
		def := evictionCommand
		def.Input = &manifest.Input{Fields: []manifest.Field{{Name: "hostname"}, {Name: "hostname"}}}
		c := seedFlags(t, map[string]string{"hostname": "host-new"})
		got := destructiveSummary(c, def, nil)
		if requests.Load() != 1 || strings.Count(got, "name=worker one") != 1 {
			t.Fatalf("summary=%s; reads=%d", got, requests.Load())
		}
	})
	t.Run("yes bypasses lookup", func(t *testing.T) {
		requests := useEvictionCase(t, evictionCases()[0])
		c := seedFlags(t, map[string]string{"hostname": "host-new"})
		c.Flags().Bool("yes", true, "")
		if err := confirmDestructive(c, evictionCommand, nil); err != nil || requests.Load() != 0 {
			t.Fatalf("err=%v; reads=%d", err, requests.Load())
		}
	})
	t.Run("missing credentials has explicit failure without warnings", func(t *testing.T) {
		useNodeNameConfig(t, &config.Config{})
		c := seedFlags(t, map[string]string{"hostname": "host-new"})
		stderr := captureStderr(t, func() {
			if got := destructiveSummary(c, evictionCommand, nil); got != "hostname=host-new"+evictionLookupFailed {
				t.Errorf("summary=%q", got)
			}
		})
		if stderr != "" {
			t.Fatal(stderr)
		}
	})
}

func TestEvictionHostnameDeadlineIncludesConfig(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		time.Sleep(time.Second)
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	useNodeNameConfig(t, patConfig(localhostURL(srv.URL)))
	cfg := patConfig(localhostURL(srv.URL))
	loadNodeNameConfig = func() (*config.Config, error) {
		time.Sleep(1500 * time.Millisecond)
		return cfg, nil
	}
	wait := waitForNodeNameWorker(t)
	defer wait()
	c := seedFlags(t, map[string]string{"hostname": "host-new", "acknowledge-data-loss": "true"})
	started := time.Now()
	stderr := captureStderr(t, func() {
		got := destructiveSummary(c, evictionCommand, nil)
		if got != "hostname=host-new"+evictionLookupFailed+" acknowledge-data-loss=true" {
			t.Errorf("summary=%s", got)
		}
	})
	if elapsed := time.Since(started); elapsed < nodeNameDeadline || elapsed > nodeNameDeadline+400*time.Millisecond {
		t.Errorf("deadline elapsed=%v", elapsed)
	}
	if stderr != "" || requests.Load() != 1 {
		t.Fatalf("stderr=%q; reads=%d", stderr, requests.Load())
	}
}
