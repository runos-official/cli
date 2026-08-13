package cmd

import (
	"errors"
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
