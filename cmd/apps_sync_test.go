package cmd

import "testing"

// Regression test for V12 (VCS_DEPLOY_TEST_NOTES.md): apps_sync's wait-on-
// each-job behaviour is gated by --no-follow + TTY. Pure helper so the
// dispatch in applySyncPlan stays a thin switch over its return.
//
// Truth table (mirrors deploy's effectiveFollow plus a streamProgress
// channel for the silent-block-vs-stream split the user's brief calls for):
//
//   noFollow | isTTY | -> follow | streamProgress
//   -------- | ----- | --------- | ---------------
//   false    | true  | true      | true   (interactive: stream lines)
//   false    | false | true      | false  (CI: silent-block, then "ok"/"failed")
//   true     | true  | false     | false  (explicit fire-and-forget)
//   true     | false | false     | false  (explicit fire-and-forget in CI)
func TestEffectiveSyncFollow(t *testing.T) {
	cases := []struct {
		name             string
		noFollow         bool
		isTTY            bool
		wantFollow       bool
		wantStreamProg   bool
	}{
		{name: "default + TTY: follow + stream (interactive)", noFollow: false, isTTY: true, wantFollow: true, wantStreamProg: true},
		{name: "default + non-TTY: follow + silent (CI)", noFollow: false, isTTY: false, wantFollow: true, wantStreamProg: false},
		{name: "--no-follow + TTY: skip wait", noFollow: true, isTTY: true, wantFollow: false, wantStreamProg: false},
		{name: "--no-follow + non-TTY: skip wait", noFollow: true, isTTY: false, wantFollow: false, wantStreamProg: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			follow, stream := effectiveSyncFollow(tc.noFollow, tc.isTTY)
			if follow != tc.wantFollow || stream != tc.wantStreamProg {
				t.Errorf("effectiveSyncFollow(noFollow=%v, isTTY=%v) = (follow=%v, stream=%v), want (follow=%v, stream=%v)",
					tc.noFollow, tc.isTTY, follow, stream, tc.wantFollow, tc.wantStreamProg)
			}
		})
	}
}
