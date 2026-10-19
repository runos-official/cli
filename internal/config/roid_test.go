package config

import (
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
			c.ApplyEnvironmentAuth(tc.auth)
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
