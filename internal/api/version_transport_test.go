package api

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/runos-official/cli/version"
)

func withVersion(t *testing.T, v string) {
	t.Helper()
	previous := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = previous })
}

func TestEveryRequestCarriesTheCLIVersionInTheUserAgent(t *testing.T) {
	withVersion(t, "1.2.3")
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path+"="+r.UserAgent())
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL)

	if _, err := c.Do("GET", "/send", "", nil); err != nil {
		t.Fatal(err)
	}
	// Device auth builds its own request and does not pass through Send.
	_, _ = c.InitiateDeviceAuth()
	_, _ = c.PollDeviceAuth("d", "t")

	agent := "runos-cli/1.2.3 (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
	want := []string{"/send=" + agent, "/auth/device/initiate=" + agent, "/auth/device/poll=" + agent}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("seen %v, want %v", seen, want)
	}
}

func TestADevBuildSendsItsOwnLabel(t *testing.T) {
	withVersion(t, "dev")
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.UserAgent()
	}))
	defer srv.Close()
	if _, err := NewClient(srv.URL).Do("GET", "/x", "", nil); err != nil || !strings.HasPrefix(got, "runos-cli/dev ") {
		t.Fatalf("user agent %q err %v", got, err)
	}
}

// Conductor reads the text between "runos-cli/" and the first space as the version, so the version
// must be the first token and the platform must come after a space.
func TestTheUserAgentIsTheVersionThenThePlatform(t *testing.T) {
	withVersion(t, "1.24.2")
	want := "runos-cli/1.24.2 (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
	if got := UserAgent(); got != want {
		t.Fatalf("UserAgent() = %q, want %q", got, want)
	}
}

// A client made with WithoutVersion (a download from a third party) sends Go's own agent and no
// CLI version, and still reaches the host.
func TestWithoutVersionSendsNoCLIVersion(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.UserAgent() }))
	defer srv.Close()
	stamped := &http.Client{Transport: NewTransport()}
	resp, err := WithoutVersion(stamped).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if strings.Contains(got, "runos-cli") {
		t.Fatalf("user agent %q carries the CLI version", got)
	}
	if stamped.Transport == nil {
		t.Fatal("WithoutVersion changed the client it was given")
	}
}

const updateRequiredBody = `{"error":"cli too old","code":"cli.update_required","minimumVersion":"9.9.9","yourVersion":"1.0.0","fix":"update"}`

func serve426(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUpgradeRequired)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAnUpdateRequiredAnswerBecomesATypedError(t *testing.T) {
	srv := serve426(t, updateRequiredBody)
	c := NewClient(srv.URL)
	calls := map[string]func() error{
		"Send": func() error { _, err := c.Do("GET", "/x", "", nil); return err },
		"Poll": func() error { _, err := c.PollDeviceAuth("d", "t"); return err },
		"Init": func() error { _, err := c.InitiateDeviceAuth(); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			var ur *UpdateRequiredError
			if !errors.As(err, &ur) {
				t.Fatalf("err = %v, want an *UpdateRequiredError in the chain", err)
			}
			msg := ur.Error()
			if !strings.Contains(msg, "runos update") || !strings.Contains(msg, "too old") || strings.ContainsAny(msg, "{}") {
				t.Fatalf("message %q, want one plain sentence naming runos update and no body", msg)
			}
			env := ur.Envelope()
			if env["code"] != UpdateRequiredCode || env["statusCode"] != http.StatusUpgradeRequired || env["error"] != msg || env["minimumVersion"] != "9.9.9" {
				t.Fatalf("envelope %v", env)
			}
		})
	}
}

func TestAnotherUpgradeRequiredAnswerPassesThroughUntouched(t *testing.T) {
	srv := serve426(t, `{"error":"use TLS","code":"proto.upgrade"}`)
	res, err := NewClient(srv.URL).Do("GET", "/x", "", nil)
	if err != nil {
		t.Fatalf("err %v, want a plain Result", err)
	}
	if res.StatusCode != 426 || !strings.Contains(string(res.Body), "proto.upgrade") {
		t.Fatalf("status %d body %q", res.StatusCode, res.Body)
	}
}

func TestAnUpgradeRequiredAnswerWithNoCodeIsNotTheGate(t *testing.T) {
	srv := serve426(t, `<html>nope</html>`)
	res, err := NewClient(srv.URL).Do("GET", "/x", "", nil)
	if err != nil || res.StatusCode != 426 {
		t.Fatalf("res %v err %v, want an ordinary 426 Result (the code is what marks the gate)", res, err)
	}
}

// roundTripFunc records what reaches a client's own transport.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func notFoundVia(agents *[]string) roundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		*agents = append(*agents, r.UserAgent())
		return &http.Response{StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	}
}

// A client that had its own transport keeps it when the version is taken off: the call still goes
// through that transport, and only the User-Agent changes.
func TestWithoutVersionKeepsTheClientsOwnTransport(t *testing.T) {
	var agents []string
	stamped := &http.Client{Transport: versionTransport{inner: notFoundVia(&agents)}}

	if resp, err := stamped.Get("https://api.example.test/x"); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
	}
	if resp, err := WithoutVersion(stamped).Get("https://other.example.test/x"); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
	}

	if len(agents) != 2 || !strings.HasPrefix(agents[0], "runos-cli/") || strings.HasPrefix(agents[1], "runos-cli/") {
		t.Fatalf("agents %v, want the first call stamped and the second not, both through the inner transport", agents)
	}
}

func TestWithoutVersionLeavesAnotherTransportAlone(t *testing.T) {
	var agents []string
	own := notFoundVia(&agents)
	plain := WithoutVersion(&http.Client{Transport: own})
	if plain.Transport == nil {
		t.Fatal("WithoutVersion dropped a transport that was not ours")
	}
	if resp, err := plain.Get("https://other.example.test/x"); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
	}
	if len(agents) != 1 {
		t.Fatalf("agents %v, want the call through the client's own transport", agents)
	}
}

// The command layer learns of a refusal the moment it happens, wherever in the command it
// happened, so it can keep cobra from printing the raw error first.
func TestTheObserverIsToldOfEveryRefusal(t *testing.T) {
	srv := serve426(t, updateRequiredBody)
	calls := 0
	SetUpdateRequiredObserver(func() { calls++ })
	t.Cleanup(func() { SetUpdateRequiredObserver(nil) })

	_, _ = NewClient(srv.URL).Do("GET", "/x", "", nil)
	other := serve426(t, `{"code":"proto.upgrade"}`)
	_, _ = NewClient(other.URL).Do("GET", "/x", "", nil)

	if calls != 1 {
		t.Fatalf("observer called %d times, want once (for the gate only)", calls)
	}
}
