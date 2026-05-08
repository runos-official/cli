package cmd

import "testing"

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
