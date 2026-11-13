package cmd

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/runos-official/cli/internal/config"
	"github.com/spf13/cobra"
)

const (
	betaConsole = "https://console.beta.example.com"
	betaAPI     = "https://api.beta.example.com"
)

// retiredCDN is a CDN config with a default (prod) that names an issuer, and a beta entry.
func retiredCDN() *config.RemoteConfig {
	return &config.RemoteConfig{
		Default: "prod",
		Environments: map[string]config.RemoteEnvironment{
			"prod": {
				Domains: config.RemoteDomains{Console: "https://console.example.com", API: "https://api.example.com"},
				Auth:    config.RemoteAuth{Provider: config.AuthProviderRoid, Issuer: refreshIssuer},
			},
			"beta": {Domains: config.RemoteDomains{Console: betaConsole, API: betaAPI}},
		},
	}
}

// signedInOnBeta is a config made on beta with credentials of every kind that belongs to it.
func signedInOnBeta() *config.Config {
	cfg := &config.Config{}
	cfg.ApplyEnvironment("beta", betaConsole, betaAPI)
	cfg.AccountID = "acct-1"
	cfg.RefreshToken = "refresh-1"
	cfg.APIKey = "key-1"
	cfg.Firebase = &config.FirebaseConfig{ProjectID: "beta-project"}
	cfg.DefaultClusterID = "cluster-1"
	cfg.KnownAccounts = []config.KnownAccount{{AccountID: "acct-1", Active: true, RefreshToken: "refresh-1"}}
	cfg.Roid = &config.RoidSignIn{Issuer: "https://id.beta.example.com", PersonID: "p_1"}
	return cfg
}

func stubEndedSignIns(t *testing.T) *int {
	t.Helper()
	ended := 0
	previous := endStoredRoidSignIn
	endStoredRoidSignIn = func(*cobra.Command, *config.Config) { ended++ }
	t.Cleanup(func() { endStoredRoidSignIn = previous })
	return &ended
}

func runMove(cfg *config.Config, report signInReporter) (string, bool) {
	cmd := &cobra.Command{}
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	moved := moveOffRetiredEnvironment(cmd, cfg, report)
	return stderr.String(), moved
}

func TestABetaEnvironmentMovesToTheCDNDefaultAndEndsTheBetaSession(t *testing.T) {
	isolateConfigEnv(t)
	stubIssuerConfig(t, retiredCDN(), nil)
	ended := stubEndedSignIns(t)
	cfg := signedInOnBeta()

	stderr, moved := runMove(cfg, textSignIn{out: &bytes.Buffer{}})

	if !moved {
		t.Fatal("a beta config was not moved")
	}
	if cfg.Env != "prod" || cfg.ConsoleURL != "https://console.example.com" || cfg.ConductorURL != "https://api.example.com" || cfg.AuthIssuer != refreshIssuer {
		t.Fatalf("environment not taken from the CDN default: %+v", cfg)
	}
	if cfg.RefreshToken != "" || cfg.APIKey != "" || cfg.Firebase != nil || cfg.Roid != nil || cfg.AccountID != "" || cfg.DefaultClusterID != "" {
		t.Fatalf("beta session carried over: %+v", cfg)
	}
	if len(cfg.KnownAccounts) != 1 || cfg.KnownAccounts[0].AccountID != "acct-1" || cfg.KnownAccounts[0].RefreshToken != "" {
		t.Fatalf("known accounts: want the entry kept and its credential gone, got %+v", cfg.KnownAccounts)
	}
	if *ended != 1 {
		t.Fatalf("the stored roid sign-in was ended %d times, want 1", *ended)
	}
	if !strings.Contains(stderr, "beta") || strings.Count(stderr, "\n") != 1 {
		t.Fatalf("stderr %q, want one line naming beta", stderr)
	}
	saved, err := config.Load()
	if err != nil || saved.Env != "prod" || saved.RefreshToken != "" || saved.AuthIssuer != refreshIssuer {
		t.Fatalf("saved config %+v err=%v", saved, err)
	}
}

func TestBetaURLsWithoutTheLabelMoveToo(t *testing.T) {
	isolateConfigEnv(t)
	stubIssuerConfig(t, retiredCDN(), nil)
	stubEndedSignIns(t)
	cfg := signedInOnBeta()
	cfg.Env, cfg.EnvAPIURL = "", ""

	if _, moved := runMove(cfg, textSignIn{out: &bytes.Buffer{}}); !moved || cfg.Env != "prod" || cfg.RefreshToken != "" {
		t.Fatalf("moved=%v cfg=%+v", moved, cfg)
	}
}

func TestACustomEnvironmentIsNeverMoved(t *testing.T) {
	isolateConfigEnv(t)
	stubIssuerConfig(t, retiredCDN(), nil)
	ended := stubEndedSignIns(t)
	cfg := signedInOnBeta()
	cfg.SetAPIURL("http://localhost:3025") // a hand-set URL that is not the beta entry

	stderr, moved := runMove(cfg, textSignIn{out: &bytes.Buffer{}})

	if moved || stderr != "" || *ended != 0 || cfg.ConductorURL != "http://localhost:3025" || cfg.RefreshToken != "refresh-1" {
		t.Fatalf("moved=%v stderr=%q ended=%d cfg=%+v, want custom left alone", moved, stderr, *ended, cfg)
	}
}

func TestAnAPIURLOverrideKeepsABetaConfigAsItIs(t *testing.T) {
	isolateConfigEnv(t)
	t.Setenv("RUNOS_API_URL", "http://localhost:3025")
	calls := stubIssuerConfig(t, retiredCDN(), nil)
	cfg := signedInOnBeta()

	if _, moved := runMove(cfg, textSignIn{out: &bytes.Buffer{}}); moved || *calls != 0 || cfg.Env != "beta" {
		t.Fatalf("moved=%v fetches=%d env=%q", moved, *calls, cfg.Env)
	}
}

func TestAnUnreadableCDNChangesNothing(t *testing.T) {
	isolateConfigEnv(t)
	stubIssuerConfig(t, nil, errTestCDNDown)
	ended := stubEndedSignIns(t)
	cfg := signedInOnBeta()

	stderr, moved := runMove(cfg, textSignIn{out: &bytes.Buffer{}})

	if moved || stderr != "" || *ended != 0 || cfg.Env != "beta" || cfg.RefreshToken != "refresh-1" || cfg.Roid == nil {
		t.Fatalf("moved=%v stderr=%q ended=%d cfg=%+v", moved, stderr, *ended, cfg)
	}
}

func TestAConfigOnTheDefaultEnvironmentNeverAsksTheCDN(t *testing.T) {
	isolateConfigEnv(t)
	calls := stubIssuerConfig(t, retiredCDN(), nil)
	cfg := &config.Config{}
	cfg.ApplyEnvironment("prod", "https://console.example.com", "https://api.example.com")
	cfg.AuthIssuer = refreshIssuer

	if _, moved := runMove(cfg, textSignIn{out: &bytes.Buffer{}}); moved || *calls != 0 {
		t.Fatalf("moved=%v fetches=%d", moved, *calls)
	}
}

// The notice is one line on stderr, and for a caller reading the JSON stream one notice event. A
// second sign-in on the moved config says nothing.
func TestTheSessionNoticeAppearsOnce(t *testing.T) {
	isolateConfigEnv(t)
	stubIssuerConfig(t, retiredCDN(), nil)
	stubEndedSignIns(t)
	cfg := signedInOnBeta()
	var stdout bytes.Buffer
	report := &jsonSignIn{out: &stdout}

	first, _ := runMove(cfg, report)
	second, moved := runMove(cfg, report)

	if strings.Count(first, "\n") != 1 || second != "" || moved {
		t.Fatalf("first=%q second=%q moved=%v", first, second, moved)
	}
	if got := strings.Count(stdout.String(), `"event":"notice"`); got != 1 || !strings.Contains(stdout.String(), "environment_retired") {
		t.Fatalf("stdout %q, want exactly one notice event", stdout.String())
	}
}

var errTestCDNDown = errors.New("cdn down")

// The notice is on screen before the revoke starts, and a revoke against a host that does not
// answer cannot hold the sign-in for longer than the bound.
func TestTheNoticeComesBeforeTheRevokeAndASlowRevokeDoesNotStallLogin(t *testing.T) {
	isolateConfigEnv(t)
	stubIssuerConfig(t, retiredCDN(), nil)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	shown := make(chan string, 1)
	previous := endStoredRoidSignIn
	previousWait := endSignInWait
	endSignInWait = 50 * time.Millisecond
	cmd := &cobra.Command{}
	var stderr safeBuffer
	cmd.SetErr(&stderr)
	endStoredRoidSignIn = func(*cobra.Command, *config.Config) {
		shown <- stderr.String()
		<-release // the retired host never answers
	}
	t.Cleanup(func() { endStoredRoidSignIn = previous; endSignInWait = previousWait })
	cfg := signedInOnBeta()

	returned := make(chan bool, 1)
	go func() { returned <- moveOffRetiredEnvironment(cmd, cfg, textSignIn{out: io.Discard}) }()
	select {
	case moved := <-returned:
		if !moved {
			t.Fatal("not moved")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the sign-in is still waiting for the revoke after 2s; the wait must be bounded")
	}
	select {
	case onScreen := <-shown:
		if !strings.Contains(onScreen, "beta") {
			t.Fatalf("on screen when the revoke started: %q, want the notice already", onScreen)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the revoke never started")
	}
}

// safeBuffer is a buffer the revoke goroutine and the test can both touch.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
