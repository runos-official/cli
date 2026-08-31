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
	"strings"
	"sync"
	"testing"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/vpn"

	"github.com/spf13/cobra"
)

/*
A whole `runos vpn up` with nothing real behind it.

WHY THIS EXISTS. The failures that produced FCR155, FCR160 and the "sign in twice" report all live
in the ORDER of the calls `vpn up` makes: which account each one is scoped to, and what happens when
conductor asks for a fresh sign-in halfway through. None of that is reachable from a unit test on a
pure function, and reproducing it by hand costs an account switch, a browser round trip and a real
tunnel every time. The operator said it plainly: it is hard to test the edge cases on a Mac.

So all three things `vpn up` talks to are faked, and the command itself is real:

  - CONDUCTOR, as an httptest server. Enrolment, the session mint and the browser device-code
    endpoints, each scriptable per test.
  - THE DAEMON, as a unix socket speaking the same one-JSON-line-each-way protocol as the real one
    (internal/vpn/socket.go). The real daemon needs root because `up` opens a tun; this one records
    what it was asked for, which is the part these tests are about.
  - GOOGLE, as an httptest server, through auth.SetEndpointsForTest.

Every call each fake receives is recorded IN ORDER, because the ordering is the defect. An assertion
that the right requests happened in the wrong order would have passed on the broken code.
*/

// ---------------------------------------------------------------------------
// The fake conductor
// ---------------------------------------------------------------------------

// enrolReply is what the fake conductor answers one enrolment with.
type enrolReply struct {
	status   int
	deviceID string
	body     string
}

// mintReply is what the fake conductor answers one session mint with.
type mintReply struct {
	status int
	body   string
}

// fakeConductor is a scriptable conductor. Each reply slice is consumed in order, and the LAST
// entry repeats, so a test scripts only the transitions it cares about.
type fakeConductor struct {
	mu sync.Mutex

	enrolReplies []enrolReply
	mintReplies  []mintReply

	// The account the browser device-code flow signs in AS. A test changes this to make the
	// confirmation come back on a different account, which is the FCR155 shape.
	signInAccount string

	// calls records every request as "verb aid[/detail]", in the order they arrived.
	calls []string

	server *httptest.Server
}

func newFakeConductor(t *testing.T) *fakeConductor {
	t.Helper()
	c := &fakeConductor{signInAccount: "aaaaa"}
	mux := http.NewServeMux()

	// POST /:aid/vpn/devices  -- enrolment, account-scoped.
	// POST /:aid/vpn/devices/:id/session -- the mint, account-scoped, and the id must be one this
	// account enrolled. The real conductor's loadDevice is account-scoped and 404s otherwise, which
	// is the whole of F2.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		switch {
		case len(parts) == 3 && parts[1] == "vpn" && parts[2] == "devices":
			c.handleEnrol(w, parts[0])
		case len(parts) == 5 && parts[1] == "vpn" && parts[2] == "devices" && parts[4] == "session":
			c.handleMint(w, parts[0], parts[3])
		default:
			c.record("unexpected " + r.Method + " " + r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no such route"}`))
		}
	})

	mux.HandleFunc("/auth/device/initiate", func(w http.ResponseWriter, r *http.Request) {
		c.record("device-auth initiate")
		respondJSON(w, http.StatusOK, `{"deviceId":"DEV123","token":"tok","expiresAt":"2030-01-01T00:00:00Z"}`)
	})
	mux.HandleFunc("/auth/device/poll", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		account := c.signInAccount
		c.mu.Unlock()
		c.record("device-auth poll -> " + account)
		respondJSON(w, http.StatusOK, fmt.Sprintf(
			`{"success":true,"customToken":"custom","accountId":%q,`+
				`"firebase":{"apiKey":"KEY","authDomain":"example.invalid","projectId":"proj"}}`,
			account,
		))
	})

	c.server = httptest.NewServer(mux)
	t.Cleanup(c.server.Close)
	return c
}

func (c *fakeConductor) handleEnrol(w http.ResponseWriter, aid string) {
	c.record("enrol " + aid)
	reply := enrolReply{status: http.StatusOK, deviceID: "device-for-" + aid}
	c.mu.Lock()
	if len(c.enrolReplies) > 0 {
		reply = c.enrolReplies[0]
		if len(c.enrolReplies) > 1 {
			c.enrolReplies = c.enrolReplies[1:]
		}
	}
	c.mu.Unlock()
	if reply.body != "" {
		respondJSON(w, reply.status, reply.body)
		return
	}
	id := reply.deviceID
	if id == "" {
		id = "device-for-" + aid
	}
	respondJSON(w, reply.status, fmt.Sprintf(`{"device":{"id":%q,"address":"192.0.2.10/32","publicKey":"pk"}}`, id))
}

func (c *fakeConductor) handleMint(w http.ResponseWriter, aid, deviceID string) {
	c.record("mint " + aid + "/" + deviceID)
	reply := mintReply{status: http.StatusCreated}
	c.mu.Lock()
	if len(c.mintReplies) > 0 {
		reply = c.mintReplies[0]
		if len(c.mintReplies) > 1 {
			c.mintReplies = c.mintReplies[1:]
		}
	}
	c.mu.Unlock()

	// THE ACCOUNT-SCOPING RULE, copied from conductor's loadDevice: a device id belongs to the
	// account that enrolled it, and any other account gets a 404. Without this the fake would
	// happily mint for the previous account's id and F2 would be untestable.
	if reply.status == http.StatusCreated && !strings.HasSuffix(deviceID, aid) {
		respondJSON(w, http.StatusNotFound,
			fmt.Sprintf(`{"error":"VPN device %s not found in account %s"}`, deviceID, aid))
		return
	}
	if reply.body != "" {
		respondJSON(w, reply.status, reply.body)
		return
	}
	respondJSON(w, reply.status, `{"token":"session-token","sessionId":"sid","expiresAt":"2030-01-01T00:00:00Z"}`)
}

func (c *fakeConductor) record(call string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, call)
}

func (c *fakeConductor) recorded() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

// signInRequiredOnMint is conductor's "confirm it is you" refusal: 403 with the machine code the
// CLI branches on.
func signInRequiredOnMint() mintReply {
	return mintReply{
		status: http.StatusForbidden,
		body:   `{"error":"A VPN session needs a sign-in from the last 5 minutes","code":"vpn.sign_in_required"}`,
	}
}

func respondJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// ---------------------------------------------------------------------------
// The fake daemon
// ---------------------------------------------------------------------------

// fakeDaemon speaks the daemon's socket protocol: one JSON Request line in, one JSON Response line
// out (see internal/vpn/socket.go). The real Daemon cannot be used here because OpUp opens a tun
// and needs root.
type fakeDaemon struct {
	mu sync.Mutex

	// keys maps an account to the public key this machine holds for it, exactly as the real
	// daemon's per-account AccountState does. A test can pre-seed it to model a machine that has
	// been signed in to one account already.
	keys map[string]string

	// calls records every op, with the account it was scoped to, in order.
	calls []string
	// upRequests records the (account, device) pairs OpUp was asked to bring up. F3 is entirely
	// about these two agreeing with what was enrolled.
	upRequests []string

	listener net.Listener
	path     string
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	d := &fakeDaemon{keys: map[string]string{}}

	// macOS caps a unix socket path at 104 bytes and t.TempDir() paths are long, so the socket
	// goes somewhere short. Same reason as internal/vpn/socket_test.go.
	d.path = filepath.Join(os.TempDir(), fmt.Sprintf("runos-up-%d.sock", os.Getpid()+len(t.Name())))
	_ = os.Remove(d.path)

	listener, err := net.Listen("unix", d.path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	d.listener = listener
	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.Remove(d.path)
	})

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go d.serve(conn)
		}
	}()
	return d
}

func (d *fakeDaemon) serve(conn net.Conn) {
	defer conn.Close()
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}
	var req vpn.Request
	resp := vpn.Response{}
	if err := json.Unmarshal(line, &req); err != nil {
		resp.Error = "malformed request"
	} else {
		resp = d.handle(req)
	}
	data, _ := json.Marshal(resp)
	_, _ = conn.Write(append(data, '\n'))
}

func (d *fakeDaemon) handle(req vpn.Request) vpn.Response {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, string(req.Op)+" "+req.AccountID)

	switch req.Op {
	case vpn.OpIdentity:
		// Mirrors the real IdentityForAccount: a key PER ACCOUNT, minted on first use.
		key, ok := d.keys[req.AccountID]
		if !ok {
			key = "key-for-" + req.AccountID
			d.keys[req.AccountID] = key
		}
		return vpn.Response{Identity: &vpn.Identity{PublicKey: key, AccountID: req.AccountID}}
	case vpn.OpRotateKey:
		key := "rotated-key-for-" + req.AccountID
		d.keys[req.AccountID] = key
		return vpn.Response{Identity: &vpn.Identity{PublicKey: key, AccountID: req.AccountID}}
	case vpn.OpUp:
		d.upRequests = append(d.upRequests, req.AccountID+"/"+req.DeviceID)
		return vpn.Response{Status: &vpn.Status{Running: true, AccountID: req.AccountID}}
	case vpn.OpLogout, vpn.OpDown:
		return vpn.Response{Status: &vpn.Status{Running: false}}
	case vpn.OpStatus:
		return vpn.Response{Status: &vpn.Status{Running: false}}
	case vpn.OpConnect:
		return vpn.Response{Status: &vpn.Status{Running: true, AccountID: req.AccountID}}
	default:
		return vpn.Response{Error: fmt.Sprintf("unknown op %q", req.Op)}
	}
}

func (d *fakeDaemon) recorded() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.calls...)
}

func (d *fakeDaemon) ups() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.upRequests...)
}

// ---------------------------------------------------------------------------
// The fake Google, and the on-disk config
// ---------------------------------------------------------------------------

// fakeFirebase answers a token refresh and a custom-token exchange. `refuse` makes every refresh
// fail, which is how a test reaches the signed-out path without deleting the credentials.
func fakeFirebase(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, http.StatusOK,
			`{"id_token":"ID","idToken":"ID","refresh_token":"REFRESH","refreshToken":"REFRESH","expires_in":"3600","expiresIn":"3600"}`)
	}))
	t.Cleanup(server.Close)
	auth.SetEndpointsForTest(t, server.URL+"/signin", server.URL+"/token")
}

// writeConfig puts a config on disk under a temp HOME. config.Load reads $HOME, so this is what
// makes the whole command run against the fakes without a seam of its own.
func writeConfig(t *testing.T, apiURL, accountID string, signedIn bool) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("RUNOS_API_KEY", "")
	if err := os.MkdirAll(filepath.Join(home, ".runos"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{
		"conductor_url": apiURL,
		"console_url":   apiURL,
		"env":           "custom",
		"account_id":    accountID,
	}
	if signedIn {
		cfg["refresh_token"] = "REFRESH"
		cfg["firebase"] = map[string]string{"api_key": "KEY", "auth_domain": "example.invalid", "project_id": "proj"}
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(filepath.Join(home, ".runos", "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// readConfig reads back what the command persisted, so a test can assert which account the machine
// ended up on.
func readConfig(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".runos", "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return out
}

// ---------------------------------------------------------------------------
// Driving the command
// ---------------------------------------------------------------------------

// upResult is what one `vpn up` run produced.
type upResult struct {
	stdout string
	err    error
}

// events parses the NDJSON sign-in stream a UI reads, ignoring the status document at the end.
func (r upResult) events() []SignInEventShape {
	var out []SignInEventShape
	for _, line := range strings.Split(r.stdout, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, `{"event"`) {
			continue
		}
		var e SignInEventShape
		if json.Unmarshal([]byte(line), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// SignInEventShape is the wire shape RunOS Desktop parses. Named and asserted here so a change to
// it breaks a test in this repo rather than silently in the app.
type SignInEventShape struct {
	Event    string `json:"event"`
	DeviceID string `json:"deviceId"`
	URL      string `json:"url"`
	Reason   string `json:"reason"`
	Message  string `json:"message"`
}

// runUp runs the real `vpn up` against the fakes. `--no-browser` because no test may open one.
func runUp(t *testing.T, socketPath string, extraArgs ...string) upResult {
	t.Helper()
	var out strings.Builder

	cmd := &cobra.Command{Use: "up", RunE: runVPNUp}
	cmd.Flags().BoolP("json", "j", false, "")
	cmd.Flags().Bool("no-browser", false, "")
	cmd.Flags().Bool("non-interactive", false, "")
	cmd.Flags().String("socket", "", "")
	cmd.SetOut(&out)
	cmd.SetErr(&strings.Builder{})

	args := append([]string{"--json", "--no-browser", "--socket", socketPath}, extraArgs...)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return upResult{stdout: out.String(), err: err}
}
