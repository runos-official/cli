package cmd

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/runos-official/cli/internal/roidapi"
)

func TestAccessSubjectParsesKindAndID(t *testing.T) {
	for _, tc := range []struct {
		in, kind, id string
		ok           bool
	}{
		{"person:p_1", "person", "p_1", true},
		{"group:ops", "group", "ops", true},
		{"key:k_9", "key", "k_9", true},
		{"p_1", "", "", false},
		{"team:x", "", "", false},
		{"person:", "", "", false},
	} {
		got, err := accessSubject(tc.in)
		if (err == nil) != tc.ok || (tc.ok && (got["kind"] != tc.kind || got["id"] != tc.id)) {
			t.Errorf("%q: got %v, %v", tc.in, got, err)
		}
	}
}

func TestAccessScopeDefaultsToTheAccountAndPrefixesShortForms(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"", "acct/a1", true},
		{"cluster/c1", "acct/a1/cluster/c1", true},
		{"acct/a1/cluster/c1/ns/web", "acct/a1/cluster/c1/ns/web", true},
		{"acct/a2/cluster/c1", "", false},
	} {
		got, err := accessScope("a1", tc.in)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("%q: got %q, %v", tc.in, got, err)
		}
	}
}

func TestAccessWindowParsesMinutesOfTheDay(t *testing.T) {
	got, err := accessWindow("09:00-17:30")
	if err != nil || got["start_min"] != 540 || got["end_min"] != 1050 {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, bad := range []string{"9-5", "25:00-26:00", "09:00", "09:60-10:00"} {
		if _, err := accessWindow(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestGrantBodyTakesARoleOrActionsNotBoth(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	base := grantFlags{subject: "person:p_1", scope: "cluster/c1"}
	f := base
	f.role = "viewer"
	body, err := grantBody("a1", f, now)
	if err != nil || body["role"] != "viewer" || body["scope"] != "acct/a1/cluster/c1" {
		t.Fatalf("role grant: %v, %v", body, err)
	}
	f = base
	f.role, f.actions = "viewer", []string{"clusters.read"}
	if _, err := grantBody("a1", f, now); err == nil {
		t.Fatal("a grant with both a role and actions was accepted")
	}
	if _, err := grantBody("a1", base, now); err == nil {
		t.Fatal("a grant with neither a role nor actions was accepted")
	}
}

func TestGrantBodyCarriesConditionsAndExpiry(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	f := grantFlags{subject: "group:ops", role: "operator", sourceRanges: []string{"192.0.2.0/24"},
		window: "08:00-18:00", requireMFA: true, minAssurance: "hardware", expiresIn: 24 * time.Hour}
	body, err := grantBody("a1", f, now)
	if err != nil {
		t.Fatal(err)
	}
	cond, _ := body["conditions"].(map[string]any)
	if cond["require_mfa"] != true || cond["min_assurance"] != "hardware" || len(cond["source_ranges"].([]string)) != 1 {
		t.Fatalf("conditions = %v", cond)
	}
	if body["expires_at"] != "2026-10-17T12:00:00Z" {
		t.Fatalf("expires_at = %v", body["expires_at"])
	}
	f.expires, f.expiresIn = "2026-12-01T00:00:00Z", time.Hour
	if _, err := grantBody("a1", f, now); err == nil {
		t.Fatal("--expires and --expires-in together were accepted")
	}
	f = grantFlags{subject: "group:ops", role: "operator", sourceRanges: []string{"not-a-cidr"}}
	if _, err := grantBody("a1", f, now); err == nil {
		t.Fatal("a bad source range was accepted")
	}
}

func TestStepUpRefusalSaysHowToConfirmAndRetry(t *testing.T) {
	err := accessRefusal(&roidapi.StepUpRequiredError{ID: "su_42", Action: "access.grant"})
	if err == nil || !strings.Contains(err.Error(), "--step-up su_42") || !strings.Contains(err.Error(), "security key") {
		t.Fatalf("err = %v", err)
	}
	err = accessRefusal(roidapi.ErrStale)
	if !errors.Is(err, roidapi.ErrStale) {
		t.Fatalf("a stale refusal lost its kind: %v", err)
	}
}

func TestStepUpPromptShowsThePageAndOpensTheBrowser(t *testing.T) {
	var out strings.Builder
	var opened string
	show := stepUpPrompt(&out, func(u string) error { opened = u; return nil })
	show(&roidapi.StepUpRequiredError{ID: "su_42", Action: "access.grant", URL: "https://id.example.test/stepup/su_42"})
	if opened != "https://id.example.test/stepup/su_42" {
		t.Fatalf("opened %q", opened)
	}
	if s := out.String(); !strings.Contains(s, "https://id.example.test/stepup/su_42") || !strings.Contains(s, "security key") {
		t.Fatalf("prompt %q", s)
	}
}

func TestStepUpPromptStillPrintsWhenNoBrowserOpens(t *testing.T) {
	var out strings.Builder
	show := stepUpPrompt(&out, func(string) error { return errors.New("no browser") })
	show(&roidapi.StepUpRequiredError{ID: "su_42", URL: "https://id.example.test/stepup/su_42"})
	if !strings.Contains(out.String(), "https://id.example.test/stepup/su_42") {
		t.Fatalf("prompt %q", out.String())
	}
}
