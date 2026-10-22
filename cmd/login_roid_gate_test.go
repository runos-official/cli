package cmd

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/config"

	"github.com/spf13/cobra"
)

// stubIssuerCheck replaces the discovery check and counts its calls.
func stubIssuerCheck(t *testing.T, err error) *int {
	t.Helper()
	calls := 0
	previous := checkRoidIssuer
	checkRoidIssuer = func(string) error { calls++; return err }
	t.Cleanup(func() { checkRoidIssuer = previous })
	return &calls
}

func TestUseRoidSignInWithoutAnIssuerNeverAsksAnyone(t *testing.T) {
	calls := stubIssuerCheck(t, errors.New("must not be called"))
	var notice bytes.Buffer
	if useRoidSignIn(&config.Config{}, &notice) {
		t.Fatal("an environment with no issuer chose roid")
	}
	if *calls != 0 || notice.Len() != 0 {
		t.Fatalf("check calls=%d notice=%q; an environment with no issuer keeps Firebase silently", *calls, notice.String())
	}
}

func TestUseRoidSignInChoosesRoidWhenTheIssuerAnswers(t *testing.T) {
	calls := stubIssuerCheck(t, nil)
	var notice bytes.Buffer
	if !useRoidSignIn(&config.Config{AuthIssuer: "https://id.example.com"}, &notice) {
		t.Fatal("a live issuer did not get the roid sign-in")
	}
	if *calls != 1 || notice.Len() != 0 {
		t.Fatalf("check calls=%d notice=%q", *calls, notice.String())
	}
}

func TestUseRoidSignInFallsBackToFirebaseForANewSignInWhenTheIssuerDoesNotAnswer(t *testing.T) {
	stubIssuerCheck(t, errors.New("https://id.example.com answered HTTP 404 for its discovery document"))
	var notice bytes.Buffer
	if useRoidSignIn(&config.Config{AuthIssuer: "https://id.example.com"}, &notice) {
		t.Fatal("a dead issuer got the roid sign-in")
	}
	for _, want := range []string{"https://id.example.com", "404", "standard sign-in"} {
		if !strings.Contains(notice.String(), want) {
			t.Fatalf("notice %q does not say %q", notice.String(), want)
		}
	}
}

// An established roid sign-in is never downgraded: if the issuer is down, the sign-in fails where
// the person can see it, instead of quietly replacing a proof-bound sign-in with Firebase.
func TestUseRoidSignInKeepsAnEstablishedSignInEvenWhenTheIssuerIsDown(t *testing.T) {
	calls := stubIssuerCheck(t, errors.New("unreachable"))
	cfg := &config.Config{
		AuthIssuer: "https://id.example.com",
		Roid:       &config.RoidSignIn{Issuer: "https://id.example.com", PersonID: "p_alice"},
	}
	var notice bytes.Buffer
	if !useRoidSignIn(cfg, &notice) {
		t.Fatal("an established roid sign-in was downgraded to Firebase")
	}
	if *calls != 0 || notice.Len() != 0 {
		t.Fatalf("check calls=%d notice=%q; an established sign-in is not gated", *calls, notice.String())
	}
}

// A sign-in made against another issuer is not established for this one.
func TestUseRoidSignInGatesASignInMadeAgainstAnotherIssuer(t *testing.T) {
	calls := stubIssuerCheck(t, errors.New("unreachable"))
	cfg := &config.Config{
		AuthIssuer: "https://id.example.com",
		Roid:       &config.RoidSignIn{Issuer: "https://other.example.com", PersonID: "p_alice"},
	}
	if useRoidSignIn(cfg, io.Discard) || *calls != 1 {
		t.Fatalf("a sign-in for another issuer counted as established (calls=%d)", *calls)
	}
}

// The wiring: against a prod-shaped environment (the config names an issuer, the issuer's host
// answers 404) `runos login` goes to the Firebase device flow, says so on stderr, and never calls
// the issuer's sign-in endpoints.
func TestLoginAgainstADeadIssuerUsesTheFirebaseFlow(t *testing.T) {
	var issuerPaths []string
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issuerPaths = append(issuerPaths, r.URL.Path)
		http.NotFound(w, r)
	}))
	t.Cleanup(issuer.Close)
	var conductorPaths []string
	conductor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conductorPaths = append(conductorPaths, r.URL.Path)
		http.Error(w, "stop here", http.StatusServiceUnavailable)
	}))
	t.Cleanup(conductor.Close)

	cfg := membershipTestConfig(t, conductor.URL)
	cfg.AuthIssuer = issuer.URL
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	c := &cobra.Command{}
	c.SetErr(&stderr)
	err := interactiveLoginReporting(c, &jsonSignIn{out: io.Discard}, false, false)
	if err == nil || !strings.Contains(err.Error(), "device auth") {
		t.Fatalf("login error = %v, want the Firebase device-auth failure from the fake conductor", err)
	}
	if len(conductorPaths) == 0 {
		t.Fatal("the Firebase device flow never reached the conductor")
	}
	if len(issuerPaths) != 1 || issuerPaths[0] != "/.well-known/openid-configuration" {
		t.Fatalf("the issuer was asked %v; only the discovery document may be requested", issuerPaths)
	}
	if !strings.Contains(stderr.String(), issuer.URL) || !strings.Contains(stderr.String(), "standard sign-in") {
		t.Fatalf("stderr %q does not tell the person which issuer did not answer and what happens instead", stderr.String())
	}
}
