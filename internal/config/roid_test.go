package config

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestApplyEnvironmentAuthPicksRoidOnlyWhenTheEnvironmentNamesIt(t *testing.T) {
	cases := []struct {
		name string
		auth RemoteAuth
		want string
	}{
		{"roid with an issuer", RemoteAuth{Provider: "roid", Issuer: "https://id.example.com"}, "https://id.example.com"},
		{"no auth block keeps Firebase", RemoteAuth{}, ""},
		{"firebase named", RemoteAuth{Provider: "firebase"}, ""},
		{"roid with no issuer is not usable", RemoteAuth{Provider: "roid"}, ""},
		{"an unknown provider is not guessed at", RemoteAuth{Provider: "other", Issuer: "https://id.example.com"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{AuthIssuer: "https://stale.example.com"}
			_ = c.ApplyEnvironmentAuth(tc.auth)
			if c.AuthIssuer != tc.want {
				t.Fatalf("AuthIssuer = %q, want %q", c.AuthIssuer, tc.want)
			}
		})
	}
}

func TestRemoteEnvironmentReadsTheAuthBlock(t *testing.T) {
	var rc RemoteConfig
	raw := `{"default":"dev","environments":{"dev":{"domains":{"console":"https://console.example.com","api":"https://api.example.com"},"auth":{"provider":"roid","issuer":"https://id.example.com"}}}}`
	if err := json.Unmarshal([]byte(raw), &rc); err != nil {
		t.Fatal(err)
	}
	if got := rc.Environments["dev"].Auth; got.Provider != "roid" || got.Issuer != "https://id.example.com" {
		t.Fatalf("auth block = %+v", got)
	}
}

func TestRoidLoginLeavesExactlyOneCredentialAndNoSecretInTheFile(t *testing.T) {
	c := &Config{AccountID: "acct-old", RefreshToken: "fb-refresh", APIKey: "pat", Firebase: &FirebaseConfig{APIKey: "k"},
		AuthIssuer: "https://id.example.com", DefaultClusterID: "c1"}
	c.ApplyRoidLogin("https://id.example.com", "p_alice", "acct1", "2026-10-15T00:00:00Z")
	if c.RefreshToken != "" || c.APIKey != "" || c.Firebase != nil {
		t.Fatalf("an older credential survived: %+v", c)
	}
	if c.Roid == nil || c.Roid.PersonID != "p_alice" || c.Roid.Issuer != "https://id.example.com" || c.AccountID != "acct1" {
		t.Fatalf("roid sign-in = %+v, account %q", c.Roid, c.AccountID)
	}
	if c.DefaultClusterID != "" {
		t.Fatal("the default cluster of another account survived the account change")
	}
	raw, _ := json.Marshal(c)
	for _, s := range []string{"rt_", "at_", "seed"} {
		if strings.Contains(string(raw), s) {
			t.Fatalf("config JSON holds %q: %s", s, raw)
		}
	}
}

func TestRoidSignInCountsOnlyForTheIssuerItWasMadeWith(t *testing.T) {
	c := &Config{AuthIssuer: "https://id.example.com"}
	c.ApplyRoidLogin("https://id.example.com", "p_alice", "", "2026-10-15T00:00:00Z")
	if !c.HasRoidSignIn() {
		t.Fatal("sign-in to the configured issuer not recognised")
	}
	c.AuthIssuer = "https://id.other.example.com"
	if c.HasRoidSignIn() {
		t.Fatal("a sign-in to one issuer answered for another")
	}
	c.AuthIssuer = ""
	if c.HasRoidSignIn() {
		t.Fatal("a roid sign-in answered for a Firebase environment")
	}
}

func TestOtherLoginsAndLogoutClearTheRoidSignIn(t *testing.T) {
	c := &Config{AuthIssuer: "https://id.example.com"}
	c.ApplyRoidLogin("https://id.example.com", "p_alice", "acct1", "2026-10-15T00:00:00Z")
	c.ApplyAPIKeyLogin("acct1", "pat", "2026-10-15T00:00:00Z")
	if c.Roid != nil {
		t.Fatal("a PAT login left the roid sign-in live")
	}
	c.ApplyRoidLogin("https://id.example.com", "p_alice", "acct1", "2026-10-15T00:00:00Z")
	c.ClearSession()
	if c.Roid != nil || c.AuthIssuer != "https://id.example.com" {
		t.Fatalf("logout: roid %+v, issuer %q (the issuer is environment and stays)", c.Roid, c.AuthIssuer)
	}
}

func TestCloneDoesNotShareTheRoidSignIn(t *testing.T) {
	c := &Config{AuthIssuer: "https://id.example.com"}
	c.ApplyRoidLogin("https://id.example.com", "p_alice", "", "2026-10-15T00:00:00Z")
	clone := c.Clone()
	clone.Roid.PersonID = "p_bob"
	if c.Roid.PersonID != "p_alice" {
		t.Fatal("changing the clone's roid sign-in changed the original")
	}
}

func TestValidateIssuerAcceptsOnlyAnOrigin(t *testing.T) {
	cases := []struct {
		issuer string
		want   string // empty: accepted
	}{
		{"https://id.example.com", ""},
		{"https://id.example.com/", ""},
		{"https://ID.Example.com:8443", ""},
		{"http://localhost:8443", ""},
		{"http://127.0.0.1:8443", ""},
		{"http://[::1]:8443", ""},
		{"http://id.example.com", "https"},
		{"http://192.0.2.1", "https"},
		{"ftp://id.example.com", "https"},
		{"https://id.example.com/realm", "no path"},
		{"https://id.example.com?x=1", "no path"},
		{"https://id.example.com#frag", "no path"},
		{"https://user:pw@id.example.com", "credentials"},
		{"https://user@id.example.com", "credentials"},
		{"id.example.com", "scheme and host"},
		{"https://", "scheme and host"},
		{"", "scheme and host"},
	}
	for _, tc := range cases {
		err := ValidateIssuer(tc.issuer)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("ValidateIssuer(%q) refused: %v", tc.issuer, err)
		case tc.want != "" && err == nil:
			t.Errorf("ValidateIssuer(%q) accepted", tc.issuer)
		case tc.want != "" && !strings.Contains(err.Error(), tc.want):
			t.Errorf("ValidateIssuer(%q) = %q, want it to say %q", tc.issuer, err, tc.want)
		}
	}
}

func TestCanonicalIssuerTrimsTheSlashAndLowercasesSchemeAndHost(t *testing.T) {
	for in, want := range map[string]string{
		"https://id.example.com":       "https://id.example.com",
		"https://id.example.com/":      "https://id.example.com",
		"HTTPS://ID.Example.COM/":      "https://id.example.com",
		"https://ID.example.com:8443/": "https://id.example.com:8443",
		"  https://id.example.com  ":   "https://id.example.com",
		"":                             "",
	} {
		if got := CanonicalIssuer(in); got != want {
			t.Errorf("CanonicalIssuer(%q) = %q, want %q", in, got, want)
		}
	}
}

// F2: the CDN config is the one place the issuer is trusted from, and a hand-edited file. An
// issuer that `config set auth-issuer` would refuse is refused here too, and the environment keeps
// the Firebase sign-in.
func TestApplyEnvironmentAuthRefusesAnIssuerThatConfigSetWouldRefuse(t *testing.T) {
	for _, issuer := range []string{
		"http://id.example.com", "http://192.0.2.1", "https://id.example.com/realm",
		"https://u:p@id.example.com", "https://id.example.com?x=1", "not a url",
	} {
		c := &Config{AuthIssuer: "https://stale.example.com"}
		err := c.ApplyEnvironmentAuth(RemoteAuth{Provider: "roid", Issuer: issuer})
		if err == nil {
			t.Errorf("issuer %q was accepted", issuer)
		}
		if c.AuthIssuer != "" {
			t.Errorf("issuer %q left AuthIssuer = %q; an invalid issuer keeps Firebase", issuer, c.AuthIssuer)
		}
	}
}

func TestApplyEnvironmentAuthStoresTheCanonicalIssuer(t *testing.T) {
	c := &Config{}
	if err := c.ApplyEnvironmentAuth(RemoteAuth{Provider: "roid", Issuer: "https://ID.example.com/"}); err != nil {
		t.Fatal(err)
	}
	if c.AuthIssuer != "https://id.example.com" {
		t.Fatalf("AuthIssuer = %q", c.AuthIssuer)
	}
}

func TestApplyEnvironmentAuthSaysNothingWhenThereIsNoRoidBlock(t *testing.T) {
	c := &Config{AuthIssuer: "https://stale.example.com"}
	for _, a := range []RemoteAuth{{}, {Provider: "firebase"}, {Provider: "roid"}, {Provider: "other", Issuer: "http://x"}} {
		if err := c.ApplyEnvironmentAuth(a); err != nil {
			t.Errorf("%+v: %v", a, err)
		}
	}
}

// F3: the same issuer written with a trailing slash or another case is the same issuer.
func TestRoidSignInCountsAcrossATrailingSlashAndCaseOfTheHost(t *testing.T) {
	c := &Config{AuthIssuer: "https://id.example.com"}
	c.Roid = &RoidSignIn{Issuer: "https://ID.example.com/", PersonID: "p_alice"}
	if !c.HasRoidSignIn() {
		t.Fatal("a sign-in recorded as https://ID.example.com/ stopped counting for https://id.example.com")
	}
	c.AuthIssuer = "https://id.example.com/"
	c.Roid.Issuer = "https://id.example.com"
	if !c.HasRoidSignIn() {
		t.Fatal("a configured issuer with a trailing slash stopped matching its sign-in")
	}
	c.AuthIssuer = "https://id.example.org"
	if c.HasRoidSignIn() {
		t.Fatal("another host counted as the same issuer")
	}
}

// An invalid CDN issuer keeps Firebase, saves the environment, and says so once.
func TestApplyRemoteEnvWithAnInvalidIssuerKeepsFirebaseAndSaysSoOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var notice bytes.Buffer
	previous := authNoticeWriter
	authNoticeWriter = &notice
	t.Cleanup(func() { authNoticeWriter = previous })

	rc := &RemoteConfig{Environments: map[string]RemoteEnvironment{"dev": {
		Auth: RemoteAuth{Provider: "roid", Issuer: "http://id.example.com"},
	}}}
	cfg, err := applyRemoteEnv(rc, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthIssuer != "" {
		t.Fatalf("AuthIssuer = %q, want Firebase", cfg.AuthIssuer)
	}
	if lines := strings.Count(strings.TrimSpace(notice.String()), "\n") + 1; lines != 1 || !strings.Contains(notice.String(), "http://id.example.com") {
		t.Fatalf("notice = %q, want one line naming the issuer", notice.String())
	}
}
