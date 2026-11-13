package cmd

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/runos-official/cli/internal/config"
)

const refreshIssuer = "https://id.example.com"

// stubIssuerConfig replaces the CDN fetch used by the refresh and counts its calls.
func stubIssuerConfig(t *testing.T, rc *config.RemoteConfig, err error) *int {
	t.Helper()
	calls := 0
	previous := fetchCDNConfig
	fetchCDNConfig = func(time.Duration) (*config.RemoteConfig, error) { calls++; return rc, err }
	resetCDNConfigCache()
	t.Cleanup(func() { fetchCDNConfig = previous; resetCDNConfigCache() })
	return &calls
}

func cdnNaming(env, issuer string) *config.RemoteConfig {
	e := config.RemoteEnvironment{}
	if issuer != "" {
		e.Auth = config.RemoteAuth{Provider: config.AuthProviderRoid, Issuer: issuer}
	}
	return &config.RemoteConfig{Default: env, Environments: map[string]config.RemoteEnvironment{env: e}}
}

func cdnEnvConfig(env string) *config.Config {
	cfg := &config.Config{}
	cfg.ApplyEnvironment(env, "https://console.example.com", "https://api.example.com")
	return cfg
}

func TestSignInRefreshesAnEmptyIssuerFromTheCDN(t *testing.T) {
	isolateConfigEnv(t)
	stubIssuerConfig(t, cdnNaming("prod", refreshIssuer), nil)
	checked := stubIssuerCheck(t, nil)
	cfg := cdnEnvConfig("prod")
	use, err := useRoidSignIn(cfg, textSignIn{out: io.Discard}, io.Discard)
	if !use || err != nil {
		t.Fatalf("use=%v err=%v, want roid after the refresh", use, err)
	}
	if cfg.AuthIssuer != refreshIssuer || *checked != 1 {
		t.Fatalf("issuer=%q checks=%d", cfg.AuthIssuer, *checked)
	}
	saved, err := config.Load()
	if err != nil || saved.AuthIssuer != refreshIssuer {
		t.Fatalf("saved config issuer=%v err=%v, want the refreshed issuer on disk", saved, err)
	}
}

func TestSignInRefreshLeavesTheIssuerEmptyWhenItMustNotRun(t *testing.T) {
	custom := cdnEnvConfig("prod")
	custom.SetAPIURL("http://localhost:3025") // hand-set API URL: env becomes custom
	cases := []struct {
		name string
		cfg  *config.Config
		rc   *config.RemoteConfig
		err  error
	}{
		{"hand-set api url", custom, cdnNaming("prod", refreshIssuer), nil},
		{"env custom", &config.Config{Env: config.EnvCustom}, cdnNaming("prod", refreshIssuer), nil},
		{"cdn failure", cdnEnvConfig("prod"), nil, errors.New("cdn down")},
		{"cdn names no issuer", cdnEnvConfig("prod"), cdnNaming("prod", ""), nil},
		{"cdn lacks the environment", cdnEnvConfig("beta"), cdnNaming("prod", refreshIssuer), nil},
		{"cdn issuer is invalid", cdnEnvConfig("prod"), cdnNaming("prod", "http://id.example.com"), nil},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			isolateConfigEnv(t)
			stubIssuerConfig(t, tt.rc, tt.err)
			checked := stubIssuerCheck(t, errors.New("must not be called"))
			use, err := useRoidSignIn(tt.cfg, textSignIn{out: io.Discard}, io.Discard)
			if use || err != nil || tt.cfg.AuthIssuer != "" || *checked != 0 {
				t.Fatalf("use=%v err=%v issuer=%q checks=%d, want the unchanged standard sign-in", use, err, tt.cfg.AuthIssuer, *checked)
			}
		})
	}
}

func TestSignInRefreshSkipsTheFetchForACustomOverride(t *testing.T) {
	isolateConfigEnv(t)
	t.Setenv("RUNOS_API_URL", "http://localhost:3025")
	calls := stubIssuerConfig(t, cdnNaming("prod", refreshIssuer), nil)
	if _, err := useRoidSignIn(cdnEnvConfig("prod"), textSignIn{out: io.Discard}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if *calls != 0 {
		t.Fatalf("fetched the CDN %d times for an API URL override", *calls)
	}
}

func TestSignInRefreshNeverReplacesAStoredIssuer(t *testing.T) {
	isolateConfigEnv(t)
	calls := stubIssuerConfig(t, cdnNaming("prod", "https://other.example.com"), nil)
	stubIssuerCheck(t, nil)
	cfg := cdnEnvConfig("prod")
	cfg.AuthIssuer = refreshIssuer
	if use, err := useRoidSignIn(cfg, textSignIn{out: io.Discard}, io.Discard); !use || err != nil {
		t.Fatalf("use=%v err=%v", use, err)
	}
	if *calls != 0 || cfg.AuthIssuer != refreshIssuer {
		t.Fatalf("calls=%d issuer=%q, want a stored issuer untouched and no fetch", *calls, cfg.AuthIssuer)
	}
}

// No test reaches the CDN. A sign-in test that does not stub the fetch gets a failed read, which
// the sign-in treats as "change nothing".
func init() {
	fetchCDNConfig = func(time.Duration) (*config.RemoteConfig, error) {
		return nil, errors.New("tests do not reach the CDN")
	}
}

// A config made before the label existed has URLs and no `env`. When its console and API URLs are
// exactly one CDN environment's, it is that environment: the issuer is filled and the label stamped.
func TestAnEmptyLabelOnACDNEnvironmentsURLsIsFilledAndLabelled(t *testing.T) {
	cases := map[string]func(cfg *config.Config){
		"no label":                func(cfg *config.Config) { cfg.Env, cfg.EnvAPIURL = "", "" },
		"label that lost its URL": func(cfg *config.Config) { cfg.EnvAPIURL = "https://api.elsewhere.example.test" },
		"label custom":            func(cfg *config.Config) { cfg.Env = config.EnvCustom },
	}
	for name, stale := range cases {
		t.Run(name, func(t *testing.T) {
			isolateConfigEnv(t)
			stubIssuerConfig(t, loadCDNFixture(t), nil)
			stubIssuerCheck(t, nil)
			cfg := cdnEnvConfig("prod")
			cfg.ConsoleURL, cfg.ConductorURL = "https://console.prod.example.test", "https://api.prod.example.test"
			stale(cfg)

			use, err := useRoidSignIn(cfg, textSignIn{out: io.Discard}, io.Discard)

			if !use || err != nil || cfg.AuthIssuer != "https://id.prod.example.test" || cfg.Env != "prod" || cfg.EnvAPIURL != cfg.ConductorURL {
				t.Fatalf("use=%v err=%v issuer=%q env=%q envAPI=%q", use, err, cfg.AuthIssuer, cfg.Env, cfg.EnvAPIURL)
			}
			saved, lerr := config.Load()
			if lerr != nil || saved.AuthIssuer != cfg.AuthIssuer || saved.Env != "prod" {
				t.Fatalf("saved %+v err %v", saved, lerr)
			}
		})
	}
}

func TestATrulyCustomConfigIsLeftAloneEvenWithRealLookingURLs(t *testing.T) {
	isolateConfigEnv(t)
	calls := stubIssuerConfig(t, loadCDNFixture(t), nil)
	cfg := &config.Config{ConsoleURL: "https://console.mine.example.test", ConductorURL: "https://api.mine.example.test"}
	if use, err := useRoidSignIn(cfg, textSignIn{out: io.Discard}, io.Discard); use || err != nil || cfg.AuthIssuer != "" || cfg.Env != "" {
		t.Fatalf("use=%v err=%v cfg=%+v", use, err, cfg)
	}
	if *calls != 1 {
		t.Fatalf("fetches %d, want one read to compare against", *calls)
	}
}

func TestTwoCDNEnvironmentsWithTheSameURLsAreNotGuessedBetween(t *testing.T) {
	isolateConfigEnv(t)
	rc := loadCDNFixture(t)
	rc.Environments["twin"] = rc.Environments["prod"]
	stubIssuerConfig(t, rc, nil)
	cfg := &config.Config{ConsoleURL: "https://console.prod.example.test", ConductorURL: "https://api.prod.example.test"}
	if _, err := useRoidSignIn(cfg, textSignIn{out: io.Discard}, io.Discard); err != nil || cfg.AuthIssuer != "" || cfg.Env != "" {
		t.Fatalf("err=%v cfg=%+v, want no change when the match is ambiguous", err, cfg)
	}
}

// A local development config (loopback or an IP literal, or plain http) cannot be a CDN entry, so
// it costs no CDN read.
func TestALocalConfigNeverReadsTheCDN(t *testing.T) {
	for _, base := range []string{"http://localhost:3025", "http://127.0.0.1:3025", "https://192.0.2.10", "http://api.prod.example.test"} {
		t.Run(base, func(t *testing.T) {
			isolateConfigEnv(t)
			calls := stubIssuerConfig(t, loadCDNFixture(t), nil)
			cfg := &config.Config{ConsoleURL: base, ConductorURL: base}
			stubEndedSignIns(t)
			runMove(cfg, textSignIn{out: io.Discard})
			if _, err := useRoidSignIn(cfg, textSignIn{out: io.Discard}, io.Discard); err != nil || *calls != 0 {
				t.Fatalf("err=%v fetches=%d, want none", err, *calls)
			}
		})
	}
}

// The beta move and the issuer refresh share one read.
func TestTheCDNIsReadOnceForBothSteps(t *testing.T) {
	isolateConfigEnv(t)
	calls := stubIssuerConfig(t, loadCDNFixture(t), nil)
	stubIssuerCheck(t, nil)
	stubEndedSignIns(t)
	cfg := &config.Config{ConsoleURL: "https://console.prod.example.test", ConductorURL: "https://api.prod.example.test"}

	runMove(cfg, textSignIn{out: io.Discard})
	if _, err := useRoidSignIn(cfg, textSignIn{out: io.Discard}, io.Discard); err != nil {
		t.Fatal(err)
	}

	if *calls != 1 || cfg.AuthIssuer == "" {
		t.Fatalf("fetches=%d issuer=%q, want one read serving both", *calls, cfg.AuthIssuer)
	}
}

// When the issuer is filled, the person is told once, on stderr and in the JSON stream.
func TestFillingTheIssuerSaysSoOnceOnStderrAndInTheStream(t *testing.T) {
	isolateConfigEnv(t)
	stubIssuerConfig(t, loadCDNFixture(t), nil)
	stubIssuerCheck(t, nil)
	cfg := cdnEnvConfig("prod")
	cfg.ConsoleURL, cfg.ConductorURL = "https://console.prod.example.test", "https://api.prod.example.test"
	var stderr, stdout bytes.Buffer

	if _, err := useRoidSignIn(cfg, &jsonSignIn{out: &stdout}, &stderr); err != nil {
		t.Fatal(err)
	}

	if strings.Count(stderr.String(), "\n") != 1 || !strings.Contains(stderr.String(), "id.prod.example.test") || !strings.Contains(stderr.String(), "identity service") {
		t.Fatalf("stderr %q, want one line naming the issuer host and roid", stderr.String())
	}
	if strings.Count(stdout.String(), `"event":"notice"`) != 1 || !strings.Contains(stdout.String(), "identity_service_enabled") {
		t.Fatalf("stdout %q, want one notice event", stdout.String())
	}

	stderr.Reset()
	if _, err := useRoidSignIn(cfg, textSignIn{out: io.Discard}, &stderr); err != nil || stderr.Len() != 0 {
		t.Fatalf("a second sign-in said %q (err %v), want silence", stderr.String(), err)
	}
}
