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
			errMsg:     "Cluster 'kqz' not found in account 'rjwrn'",
			defaultCid: "kqz",
			want:       verdictDefaultClusterMissing,
		},
		{
			// A caller who passed --cid explicitly got exactly what they asked for. Telling them
			// their config is wrong would send them to the wrong place, which is this finding's
			// own failure mode inverted.
			name:       "a DIFFERENT cluster was named, so the default is not implicated",
			errMsg:     "Cluster 'abc' not found in account 'rjwrn'",
			defaultCid: "kqz",
			want:       verdictNotStaleCluster,
		},
		{
			name:       "no default configured, so nothing can be stale",
			errMsg:     "Cluster 'kqz' not found in account 'rjwrn'",
			defaultCid: "",
			want:       verdictNotStaleCluster,
		},
		{
			name:       "an unrelated failure is left alone",
			errMsg:     "failed to create VM: 500 Internal Server Error",
			defaultCid: "kqz",
			want:       verdictNotStaleCluster,
		},
		{
			// Must not fire on a message that merely happens to contain the id as a substring of
			// something else. The quoting is what makes the match safe.
			name:       "an unquoted mention does not count",
			errMsg:     "Cluster 'kqz2' not found in account 'rjwrn'",
			defaultCid: "kqz",
			want:       verdictNotStaleCluster,
		},
		{
			name:       "double-quoted messages match too",
			errMsg:     `Cluster "kqz" not found in account "rjwrn"`,
			defaultCid: "kqz",
			want:       verdictDefaultClusterMissing,
		},
		{
			// A not-found for something that is not a cluster must not be blamed on the default.
			name:       "a different not-found is not a cluster problem",
			errMsg:     "Node 'kqz' was not found",
			defaultCid: "kqz",
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
