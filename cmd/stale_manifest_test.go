package cmd

import (
	"errors"
	"strings"
	"testing"
)

func TestJudgeStaleManifest(t *testing.T) {
	// The whole point is telling "this command does not exist" apart from "your cached
	// command list is old". Guessing wrong sent an agent to debug the API eight times.
	tests := []struct {
		name      string
		cached    string
		server    string
		serverErr error
		want      staleManifestVerdict
	}{
		{
			name:   "versions match, so the command really is unknown",
			cached: "38.2.0",
			server: "38.2.0",
			want:   verdictCommandUnknown,
		},
		{
			name:   "cache is behind, which is the case that had been misread as a failed deploy",
			cached: "35.9.0",
			server: "35.10.0",
			want:   verdictCacheStale,
		},
		{
			name:   "cache is AHEAD, which is still a mismatch worth refreshing rather than trusting",
			cached: "38.3.0",
			server: "38.2.0",
			want:   verdictCacheStale,
		},
		{
			// Telling an offline user their cache is stale would be a guess, and the point of
			// this code is to stop guessing.
			name:      "server unreachable is never reported as stale",
			cached:    "38.2.0",
			serverErr: errors.New("dial tcp: lookup failed"),
			want:      verdictCannotTell,
		},
		{
			name:   "an empty server version is not treated as a mismatch",
			cached: "38.2.0",
			server: "",
			want:   verdictCannotTell,
		},
		{
			// No cache file at all. Still a mismatch against a real server version, and
			// refreshing is exactly right.
			name:   "no cached version with a real server version reads as stale",
			cached: "",
			server: "38.2.0",
			want:   verdictCacheStale,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := judgeStaleManifest(tt.cached, tt.server, tt.serverErr)
			if got != tt.want {
				t.Errorf("judgeStaleManifest(%q, %q, %v) = %v, want %v",
					tt.cached, tt.server, tt.serverErr, got, tt.want)
			}
		})
	}
}

func TestIsUnknownCommandError(t *testing.T) {
	// Deliberately narrow. Re-fetching the manifest in response to an unrelated failure
	// would be worse than useless, so anything unrecognised stays an ordinary error.
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "cobra's unknown command, the exact string that started this",
			err:  errors.New(`unknown command "virt" for "runos"`),
			want: true,
		},
		{
			// The symptom when the dynamic tree failed to build: the parent exists but its
			// manifest-driven flags do not.
			name: "unknown flag",
			err:  errors.New("unknown flag: --cid"),
			want: true,
		},
		{
			name: "unknown shorthand flag",
			err:  errors.New(`unknown shorthand flag: 'x' in -x`),
			want: true,
		},
		{
			name: "an ordinary API failure is left alone",
			err:  errors.New("failed to create VM: 500 Internal Server Error"),
			want: false,
		},
		{
			// Must not match on a message that merely mentions the words.
			name: "a message that only contains the phrase is not a prefix match",
			err:  errors.New("the server rejected an unknown command in the payload"),
			want: false,
		},
		{
			name: "nil is never an unknown command",
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUnknownCommandError(tt.err); got != tt.want {
				t.Errorf("isUnknownCommandError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestUnknownSubjectWording is the goal-21 B6 regression. An unknown
// FLAG got the unknown-COMMAND wording, "this command really does not
// exist", while the command plainly did exist and only the flag was
// wrong. An agent reading that goes and looks for a missing deploy.
func TestUnknownSubjectWording(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"unknown command", errors.New("unknown command \"virt\" for \"runos\""), "command"},
		{"unknown flag", errors.New("unknown flag: --gpus"), "flag"},
		{"unknown shorthand flag", errors.New("unknown shorthand flag: 'z' in -z"), "flag"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := unknownSubject(c.err); got != c.want {
				t.Errorf("unknownSubject(%v) = %q, want %q", c.err, got, c.want)
			}
		})
	}
	if got := unknownSubject(errors.New("some other failure")); got != "" {
		t.Errorf("an unrelated error names no subject, got %q", got)
	}
}

// TestManifestDriftGuidance is the goal-21 B7 regression. A 4xx from a
// dispatch (a route the server does not have, a field it does not know)
// looked identical whether the CLI's cached command list was current or
// months behind, and nothing compared the two.
func TestManifestDriftGuidance(t *testing.T) {
	t.Run("drift produces guidance naming both versions", func(t *testing.T) {
		got := manifestDriftGuidance("40.1.0", "40.7.0")
		for _, want := range []string{"40.1.0", "40.7.0", "manifest update"} {
			if !strings.Contains(got, want) {
				t.Errorf("expected %q in the guidance, got: %s", want, got)
			}
		}
	})
	t.Run("no drift produces nothing", func(t *testing.T) {
		if got := manifestDriftGuidance("40.7.0", "40.7.0"); got != "" {
			t.Errorf("expected no guidance when the versions agree, got: %s", got)
		}
	})
	t.Run("an unknown version produces nothing", func(t *testing.T) {
		if got := manifestDriftGuidance("", "40.7.0"); got != "" {
			t.Errorf("expected no guidance without a cached version, got: %s", got)
		}
		if got := manifestDriftGuidance("40.7.0", ""); got != "" {
			t.Errorf("expected no guidance without a server version, got: %s", got)
		}
	})
}

// The drift check only runs for a client error. A 500 says nothing about
// the command list, and a 200 has nothing to explain.
func TestDriftCheckAppliesToClientErrors(t *testing.T) {
	cases := []struct {
		status int
		want   bool
	}{{400, true}, {404, true}, {409, true}, {422, true}, {401, false}, {403, false}, {500, false}, {200, false}}
	for _, c := range cases {
		if got := driftCheckApplies(c.status); got != c.want {
			t.Errorf("driftCheckApplies(%d) = %v, want %v", c.status, got, c.want)
		}
	}
}
