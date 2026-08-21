package workspace

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

// The protocol here was read from the runostty source, not assumed. These pin the three rules that
// are not guessable, so a later change that "tidies" one of them fails here instead of in a
// customer's terminal.

func TestTheUrlAlwaysNamesTheDevopsShell(t *testing.T) {
	// THE DEFECT THIS PREVENTS, and it is silent. The server falls back to the CODING shell for a
	// user it does not recognise, and an ABSENT user counts as unrecognised. So a request that
	// forgot to say devops does not fail: it opens a shell with no kubectl, and nothing anywhere
	// explains why the caller's first command was not found.
	for _, given := range []Target{
		{Host: "h.example.com", User: UserDevOps},
		{Host: "h.example.com"}, // caller left it empty
	} {
		raw, err := DialURL(given)
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := u.Query().Get("user"); got != UserDevOps {
			t.Fatalf("the shell must always be named explicitly, got user=%q from %+v", got, given)
		}
	}
}

func TestTheDefaultIsTheDevopsShellAndNotTheServersOwn(t *testing.T) {
	// The server's own default is the coding shell. Ours is deliberately NOT the same, so this
	// pins the difference rather than leaving it to be "tidied" back into agreement later.
	if DefaultUser != UserDevOps {
		t.Fatalf("DefaultUser must be %s, got %s", UserDevOps, DefaultUser)
	}
	if UserDev == UserDevOps {
		t.Fatal("the two shells must stay distinct names")
	}
}

func TestTheKeyNeverReachesTheUrl(t *testing.T) {
	// runostty REFUSES a key in the query string, and the reason is that a URL is written into the
	// ingress access log, the browser's history and any Referer a page leaks. Putting it there
	// would not just be poor practice, it would fail.
	raw, err := DialURL(Target{Host: "runostty-abc.v6b.example.com", Key: "SUPERSECRET", User: UserDev})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "SUPERSECRET") {
		t.Fatalf("the key must never appear in the URL, got %q", raw)
	}
	// It travels here instead.
	protos := Subprotocols("SUPERSECRET")
	if len(protos) != 2 {
		t.Fatalf("two subprotocols must be offered, got %v", protos)
	}
	if protos[0] != "runos.tty.v1" {
		t.Fatalf("the plain protocol must be offered, or the server selects nothing: %v", protos)
	}
	if protos[1] != "runos.psk.SUPERSECRET" {
		t.Fatalf("the second must carry the key: %v", protos)
	}
}

func TestTheUrlIsWssOnTheWorkspaceHost(t *testing.T) {
	raw, err := DialURL(Target{Host: "runostty-abc.v6b.example.com", User: UserDevOps})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "wss" {
		t.Fatalf("must be wss, got %q", u.Scheme)
	}
	if u.Host != "runostty-abc.v6b.example.com" {
		t.Fatalf("wrong host: %q", u.Host)
	}
	if u.Path != "/" {
		t.Fatalf("the terminal is at /, got %q", u.Path)
	}
	if u.Query().Get("user") != UserDevOps {
		t.Fatalf("the chosen shell must be carried: %q", u.RawQuery)
	}
}

func TestAHostThatAlreadyCarriesASchemeIsNotDoubled(t *testing.T) {
	// Conductor returns a bare hostname today. If that ever changes, this must not produce
	// wss://https://runostty-... which fails with an unreadable parse error.
	raw, err := DialURL(Target{Host: "https://runostty-abc.example.com", User: UserDev})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "https://") || strings.Count(raw, "://") != 1 {
		t.Fatalf("scheme was doubled or left wrong: %q", raw)
	}
	if !strings.HasPrefix(raw, "wss://") {
		t.Fatalf("must upgrade to wss, got %q", raw)
	}
}

func TestAOneShotCommandIsCarriedEncoded(t *testing.T) {
	// The server base64-decodes this and runs it before the prompt. Raw would break on the first
	// command containing a space, a quote or a pipe, which is most of them.
	cmd := `kubectl get nodes -o wide | grep "Ready"`
	raw, err := DialURL(Target{Host: "h.example.com", User: UserDevOps, Command: cmd})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	got, err := base64.StdEncoding.DecodeString(u.Query().Get("cmd"))
	if err != nil {
		t.Fatalf("cmd was not base64: %v", err)
	}
	if string(got) != cmd {
		t.Fatalf("the command did not survive: %q", string(got))
	}
	// Without a command the parameter must be ABSENT, not empty: the server checks presence.
	raw2, _ := DialURL(Target{Host: "h.example.com", User: UserDev})
	u2, _ := url.Parse(raw2)
	if _, present := u2.Query()["cmd"]; present {
		t.Fatal("an interactive session must not carry a cmd parameter at all")
	}
}

func TestAnEmptyHostIsRefused(t *testing.T) {
	if _, err := DialURL(Target{User: UserDev}); err == nil {
		t.Fatal("no address must be refused, not dialled as an empty host")
	}
}

func TestResizeIsExactlyTheShapeTheServerRecognises(t *testing.T) {
	b, ok := ResizeFrame(120, 40)
	if !ok {
		t.Fatal("a valid size must produce a frame")
	}
	var parsed map[string]any
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("the resize must be JSON: %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("exactly cols and rows, got %v", parsed)
	}
	if parsed["cols"] != float64(120) || parsed["rows"] != float64(40) {
		t.Fatalf("wrong values: %v", parsed)
	}
}

func TestAZeroOrNegativeSizeIsNeverSent(t *testing.T) {
	// The server refuses a dimension below 1, and a REFUSED resize falls through its JSON check
	// and is written into the shell. So sending one does not silently do nothing: it types
	// {"cols":0,"rows":24} at the user's prompt.
	for _, c := range [][2]int{{0, 24}, {80, 0}, {-1, 24}, {0, 0}} {
		if _, ok := ResizeFrame(c[0], c[1]); ok {
			t.Fatalf("%dx%d must not be sent", c[0], c[1])
		}
	}
}

func TestAOneShotCommandEndsTheSession(t *testing.T) {
	// MEASURED 2026-08-21 against a live workspace, and it is why this exists. The server runs a
	// supplied command as `bash --login -c "<cmd>; exec bash --login"`, so when the command
	// finishes it hands over an INTERACTIVE shell. Without this the caller gets their output, then
	// a prompt, and then waits forever.
	got := OneShot("kubectl get nodes")
	if got != "kubectl get nodes; exit" {
		t.Fatalf("a one-shot must end the shell, got %q", got)
	}
	// An interactive session must NOT be given one, or it exits the moment it opens.
	if OneShot("") != "" {
		t.Fatalf("an interactive session must carry no command, got %q", OneShot(""))
	}
}

func TestTheOneShotSuffixSurvivesEncoding(t *testing.T) {
	// The two pieces are separate on purpose: OneShot decides the shape, DialURL carries it. This
	// pins that they still agree, so a command with quotes and pipes arrives whole AND ends.
	cmd := `kubectl get nodes -o wide | grep "Ready"`
	raw, err := DialURL(Target{Host: "h.example.com", User: UserDevOps, Command: OneShot(cmd)})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	decoded, err := base64.StdEncoding.DecodeString(u.Query().Get("cmd"))
	if err != nil {
		t.Fatalf("cmd was not base64: %v", err)
	}
	if string(decoded) != cmd+"; exit" {
		t.Fatalf("the command did not survive intact with its exit: %q", string(decoded))
	}
}
