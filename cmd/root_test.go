package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// Regression test for V6 (VCS_DEPLOY_TEST_NOTES.md): the manifest-bootstrap
// skip-list must NOT trigger a fetch for help/version/config/update/manifest
// commands (those don't need the manifest, would surprise the user with a
// network call), but MUST trigger for deploy/apps/services/etc. so a fresh
// CI install lands a manifest before the first manifest-driven command runs.
func TestShouldBootstrapManifest(t *testing.T) {
	cases := []struct {
		name            string
		cmdName         string
		parentName      string
		manifestPresent bool
		want            bool
	}{
		{name: "manifest already on disk, never bootstrap", cmdName: "deploy", manifestPresent: true, want: false},
		{name: "deploy on fresh install bootstraps", cmdName: "deploy", manifestPresent: false, want: true},
		{name: "apps_pull on fresh install bootstraps", cmdName: "pull", parentName: "apps", manifestPresent: false, want: true},
		{name: "services_sync on fresh install bootstraps", cmdName: "sync", parentName: "services", manifestPresent: false, want: true},
		// Skip cases — these commands shouldn't trigger a network call on first run.
		{name: "version is silent", cmdName: "version", manifestPresent: false, want: false},
		{name: "help is silent", cmdName: "help", manifestPresent: false, want: false},
		{name: "config is silent", cmdName: "config", manifestPresent: false, want: false},
		{name: "config env is silent", cmdName: "env", parentName: "config", manifestPresent: false, want: false},
		{name: "update is silent", cmdName: "update", manifestPresent: false, want: false},
		// `runos manifest update` does its own fetch via ForceUpdate; the
		// bootstrap would double-fetch.
		{name: "manifest update skips bootstrap (avoids double-fetch)", cmdName: "update", parentName: "manifest", manifestPresent: false, want: false},
		{name: "any subcommand of manifest skips bootstrap", cmdName: "show", parentName: "manifest", manifestPresent: false, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldBootstrapManifest(tc.cmdName, tc.parentName, tc.manifestPresent)
			if got != tc.want {
				t.Errorf("shouldBootstrapManifest(%q, %q, present=%v) = %v, want %v", tc.cmdName, tc.parentName, tc.manifestPresent, got, tc.want)
			}
		})
	}
}

// loginNudgeApplies must stay silent for the bare root command (it shows
// the welcome banner) and for sign-in / self-guiding commands, but nudge
// for manifest-driven commands a signed-out user might run by mistake.
func TestLoginNudgeApplies(t *testing.T) {
	cases := []struct {
		cmdName string
		want    bool
	}{
		{cmdName: "runos", want: false},
		{cmdName: "login", want: false},
		{cmdName: "logout", want: false},
		{cmdName: "config", want: false},
		{cmdName: "env", want: false},
		{cmdName: "version", want: false},
		{cmdName: "help", want: false},
		{cmdName: "update", want: false},
		{cmdName: "mcp", want: false},
		{cmdName: "show", want: true},
		{cmdName: "list", want: true},
		{cmdName: "deploy", want: true},
		{cmdName: "sync", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.cmdName, func(t *testing.T) {
			if got := loginNudgeApplies(tc.cmdName); got != tc.want {
				t.Errorf("loginNudgeApplies(%q) = %v, want %v", tc.cmdName, got, tc.want)
			}
		})
	}
}

// printWelcome must never leak the local-dev-only `runos config env`
// affordance into user-facing first-run output (it's a public repo).
func TestPrintWelcomeOmitsEnv(t *testing.T) {
	var buf bytes.Buffer
	printWelcome(&buf)
	out := buf.String()
	if strings.Contains(out, "config env") {
		t.Errorf("welcome message leaks 'config env': %q", out)
	}
	if !strings.Contains(out, "runos login") {
		t.Errorf("welcome message should point at 'runos login': %q", out)
	}
}
