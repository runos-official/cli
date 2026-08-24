package cmd

import "testing"

func TestJudgeStaleCluster(t *testing.T) {
	tests := []struct {
		name       string
		errMsg     string
		defaultCid string
		want       staleClusterVerdict
	}{
		{
			// The exact message from the finding's second occurrence, which read as a broken
			// cluster rather than a stale setting.
			name:       "the message from the finding, with that cluster as the default",
			errMsg:     "Cluster 'cl2' not found in account 'acct1'",
			defaultCid: "cl2",
			want:       verdictDefaultClusterMissing,
		},
		{
			// A caller who passed --cid explicitly got exactly what they asked for. Telling them
			// their config is wrong would send them to the wrong place, which is this finding's
			// own failure mode inverted.
			name:       "a DIFFERENT cluster was named, so the default is not implicated",
			errMsg:     "Cluster 'abc' not found in account 'acct1'",
			defaultCid: "cl2",
			want:       verdictNotStaleCluster,
		},
		{
			name:       "no default configured, so nothing can be stale",
			errMsg:     "Cluster 'cl2' not found in account 'acct1'",
			defaultCid: "",
			want:       verdictNotStaleCluster,
		},
		{
			name:       "an unrelated failure is left alone",
			errMsg:     "failed to create VM: 500 Internal Server Error",
			defaultCid: "cl2",
			want:       verdictNotStaleCluster,
		},
		{
			// Must not fire on a message that merely happens to contain the id as a substring of
			// something else. The quoting is what makes the match safe.
			name:       "an unquoted mention does not count",
			errMsg:     "Cluster 'cl22' not found in account 'acct1'",
			defaultCid: "cl2",
			want:       verdictNotStaleCluster,
		},
		{
			name:       "double-quoted messages match too",
			errMsg:     `Cluster "cl2" not found in account "acct1"`,
			defaultCid: "cl2",
			want:       verdictDefaultClusterMissing,
		},
		{
			// A not-found for something that is not a cluster must not be blamed on the default.
			name:       "a different not-found is not a cluster problem",
			errMsg:     "Node 'cl2' was not found",
			defaultCid: "cl2",
			want:       verdictNotStaleCluster,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := judgeStaleCluster(tt.errMsg, tt.defaultCid); got != tt.want {
				t.Errorf("judgeStaleCluster(%q, %q) = %v, want %v",
					tt.errMsg, tt.defaultCid, got, tt.want)
			}
		})
	}
}

func TestNullFlagName(t *testing.T) {
	// O11: the caller who guessed RIGHT (`null`) gets a parse error, while the caller who guessed
	// wrong (`0`) silently freezes the group instead of unbounding it.
	tests := []struct {
		name   string
		errMsg string
		want   string
	}{
		{
			name:   "pflag's message for the exact command in the finding",
			errMsg: `invalid argument "null" for "--max-vcpus" flag: strconv.ParseInt: parsing "null": invalid syntax`,
			want:   "--max-vcpus",
		},
		{
			name:   "another nullable quota field",
			errMsg: `invalid argument "null" for "--max-memory-mi" flag: strconv.ParseInt: parsing "null": invalid syntax`,
			want:   "--max-memory-mi",
		},
		{
			// A different bad value is an ordinary mistake, not the nullable trap.
			name:   "a non-null parse failure is left alone",
			errMsg: `invalid argument "abc" for "--max-vcpus" flag: strconv.ParseInt: parsing "abc": invalid syntax`,
			want:   "",
		},
		{
			name:   "an unrelated error is left alone",
			errMsg: "failed to reach the API: connection refused",
			want:   "",
		},
		{
			name:   "a truncated message does not panic or invent a flag",
			errMsg: `invalid argument "null" for `,
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nullFlagName(tt.errMsg); got != tt.want {
				t.Errorf("nullFlagName(%q) = %q, want %q", tt.errMsg, got, tt.want)
			}
		})
	}
}
