package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/desktopcontract"
	"github.com/runos-official/cli/internal/vpn"

	"github.com/spf13/cobra"
)

// daemonReplying serves one fixed status on a unix socket, in the daemon's one-line protocol, so
// `runos status` reports the vpn* keys. The fake in vpn_up_harness_test.go cannot set the account.
func daemonReplying(t *testing.T, status vpn.Status) string {
	t.Helper()
	path := filepath.Join(os.TempDir(), fmt.Sprintf("runos-ct-%d.sock", os.Getpid()+len(t.Name())))
	_ = os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close(); _ = os.Remove(path) })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = bufio.NewReader(conn).ReadBytes('\n')
				data, _ := json.Marshal(vpn.Response{Status: &status})
				_, _ = conn.Write(append(data, '\n'))
			}()
		}
	}()
	return path
}

// statusDocument runs the real `runos status --json` against a socket and returns the raw bytes.
func statusDocument(t *testing.T, socket string) []byte {
	t.Helper()
	var runErr error
	out := captureStdout(t, func() {
		cmd := &cobra.Command{Use: "status", RunE: runCLIStatus}
		cmd.Flags().BoolP("json", "j", false, "")
		cmd.Flags().String("socket", "", "")
		cmd.SetArgs([]string{"--json", "--socket", socket})
		runErr = cmd.Execute()
	})
	if runErr != nil {
		t.Fatalf("status failed: %v\n%s", runErr, out)
	}
	return []byte(out)
}

// `runos status --json` builds a map, so no Go nil can leak, but the keys, their types and the
// timestamp format are still a contract: Desktop decodes `authenticated` as a non-optional Bool.
func TestDesktopContractStatusJSONMatchesTheDesktopContract(t *testing.T) {
	const expired = `{"error":"Your session has expired. Run ` + "`runos login`" + ` to sign in again.","code":"auth.session_expired"}`
	cases := []struct {
		name   string
		setup  func(t *testing.T)
		daemon vpn.Status
	}{
		{"signed out", func(t *testing.T) {
			writeConfig(t, profileConductor(t, http.StatusOK, `{}`, "").URL, "aaaaa", false)
		}, vpn.Status{}},
		{"signed in with profile, expiry and a VPN on another account", func(t *testing.T) {
			fakeFirebase(t)
			writeConfig(t, profileConductor(t, http.StatusOK,
				`{"companyName":"Example Co","website":"https://example.test"}`, "2030-01-01T00:00:00Z").URL, "aaaaa", true)
		}, vpn.Status{Running: true, AccountID: "bbbbb"}},
		{"session aged out", func(t *testing.T) {
			fakeFirebase(t)
			writeConfig(t, profileConductor(t, http.StatusUnauthorized, expired, "").URL, "aaaaa", true)
		}, vpn.Status{}},
		{"token refused", func(t *testing.T) {
			fakeFirebase(t)
			writeConfig(t, profileConductor(t, http.StatusUnauthorized, `{"error":"Invalid token"}`, "").URL, "aaaaa", true)
		}, vpn.Status{}},
		{"sign-in service unreachable", func(t *testing.T) {
			dead := httptest.NewServer(http.NotFoundHandler())
			dead.Close()
			auth.SetEndpointsForTest(t, dead.URL, dead.URL)
			writeConfig(t, profileConductor(t, http.StatusOK, `{}`, "").URL, "aaaaa", true)
		}, vpn.Status{}},
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup(t)
			doc := statusDocument(t, daemonReplying(t, tc.daemon))
			desktopcontract.Check(t, tc.name, doc, desktopcontract.CLIStatus)
			var keys map[string]any
			_ = json.Unmarshal(doc, &keys)
			for key := range keys {
				seen[key] = true
			}
			// Desktop puts authError in front of the person as it is.
			if text, ok := keys["authError"].(string); ok {
				for _, problem := range desktopcontract.PlainErrorProblems(text) {
					t.Errorf("authError %q: %s", text, problem)
				}
			}
		})
	}
	// The cases above must between them reach the keys Desktop branches on, or they prove nothing.
	for _, key := range []string{"authenticated", "accountId", "companyName", "vpnAccountId", "vpnAccountMismatch",
		"vpnRunning", "authError", "authErrorKind", "sessionExpired", "sessionExpiresAt"} {
		if !seen[key] {
			t.Errorf("no case produced %q: the table row for it is untested", key)
		}
	}
}
