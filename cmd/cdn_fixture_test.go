package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/runos-official/cli/internal/config"
)

// loadCDNFixture parses cmd/testdata/cdn_config.json, which has the shape of the real CDN config
// (a default, environments with domains and an optional auth block, the legacy "conductor" key on
// one entry) with example.test names, through the same type the CLI parses the real one with.
func loadCDNFixture(t *testing.T) *config.RemoteConfig {
	t.Helper()
	raw, err := os.ReadFile("testdata/cdn_config.json")
	if err != nil {
		t.Fatal(err)
	}
	var rc config.RemoteConfig
	if err := json.Unmarshal(raw, &rc); err != nil {
		t.Fatalf("the fixture does not parse as the CDN config: %v", err)
	}
	return &rc
}

func TestTheFixtureHasTheShapeOfTheRealConfig(t *testing.T) {
	rc := loadCDNFixture(t)
	if rc.Default != "prod" || len(rc.Environments) != 3 {
		t.Fatalf("fixture %+v", rc)
	}
	if got := rc.Environments["beta"].Domains.APIURL(); got != "https://api.beta.example.test" {
		t.Fatalf("legacy conductor key not read: %q", got)
	}
	if rc.Environments["beta"].Auth.Issuer != "" || rc.Environments["prod"].Auth.Provider != config.AuthProviderRoid {
		t.Fatalf("auth blocks %+v", rc.Environments)
	}
}

// A stale prod config with no label and no issuer signs in through roid after the refresh.
func TestTheRefreshAgainstTheFixtureFillsAStaleProdConfig(t *testing.T) {
	isolateConfigEnv(t)
	stubIssuerConfig(t, loadCDNFixture(t), nil)
	stubIssuerCheck(t, nil)
	cfg := &config.Config{ConsoleURL: "https://console.prod.example.test", ConductorURL: "https://api.prod.example.test"}

	use, err := useRoidSignIn(cfg, textSignIn{out: &bytes.Buffer{}}, &bytes.Buffer{})

	if !use || err != nil || cfg.AuthIssuer != "https://id.prod.example.test" || cfg.Env != "prod" {
		t.Fatalf("use=%v err=%v issuer=%q env=%q", use, err, cfg.AuthIssuer, cfg.Env)
	}
}

// A beta config moves to the default, and lands on its issuer.
func TestTheBetaMoveAgainstTheFixtureLandsOnProd(t *testing.T) {
	isolateConfigEnv(t)
	stubIssuerConfig(t, loadCDNFixture(t), nil)
	stubEndedSignIns(t)
	cfg := &config.Config{}
	cfg.ApplyEnvironment("beta", "https://console.beta.example.test", "https://api.beta.example.test")
	cfg.RefreshToken = "refresh-1"

	if _, moved := runMove(cfg, textSignIn{out: &bytes.Buffer{}}); !moved {
		t.Fatal("not moved")
	}
	if cfg.Env != "prod" || cfg.ConductorURL != "https://api.prod.example.test" || cfg.AuthIssuer != "https://id.prod.example.test" || cfg.RefreshToken != "" {
		t.Fatalf("cfg %+v", cfg)
	}
}

// A dev config keeps its own issuer through both steps.
func TestADevConfigIsLeftOnDevByBothSteps(t *testing.T) {
	isolateConfigEnv(t)
	stubIssuerConfig(t, loadCDNFixture(t), nil)
	stubEndedSignIns(t)
	stubIssuerCheck(t, nil)
	cfg := &config.Config{}
	cfg.ApplyEnvironment("dev", "https://console.dev.example.test", "https://api.dev.example.test")

	if _, moved := runMove(cfg, textSignIn{out: &bytes.Buffer{}}); moved {
		t.Fatal("dev was moved")
	}
	if _, err := useRoidSignIn(cfg, textSignIn{out: &bytes.Buffer{}}, &bytes.Buffer{}); err != nil || cfg.Env != "dev" || cfg.AuthIssuer != "https://id.dev.example.test" {
		t.Fatalf("err=%v env=%q issuer=%q", err, cfg.Env, cfg.AuthIssuer)
	}
}
