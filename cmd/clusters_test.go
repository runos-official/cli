package cmd

import (
	"strings"
	"testing"
)

// Regression test for issue 36: `runos clusters default <cid>` stored
// any string verbatim, including whitespace-padded and path-traversal
// inputs, with no shape check. normalizeClusterID is the offline arm of
// the fix (trim + charset). The existence check is exercised via the
// extractClusterIDs helper below.
func TestNormalizeClusterID(t *testing.T) {
	t.Run("trims surrounding whitespace", func(t *testing.T) {
		got, err := normalizeClusterID("  mycluster2  ")
		if err != nil {
			t.Fatalf("normalizeClusterID(\"  mycluster2  \") returned err=%v, want nil", err)
		}
		if got != "mycluster2" {
			t.Errorf("normalizeClusterID(\"  mycluster2  \") = %q, want %q", got, "mycluster2")
		}
	})

	t.Run("rejects empty after trim", func(t *testing.T) {
		for _, in := range []string{"", "   ", "\t\n"} {
			if _, err := normalizeClusterID(in); err == nil {
				t.Errorf("normalizeClusterID(%q) returned nil err, want refusal", in)
			}
		}
	})

	t.Run("accepts conductor identifier alphabet", func(t *testing.T) {
		for _, in := range []string{"mycluster2", "mycluster3", "ABC123", "abc-def", "abc_def", "a", "0"} {
			got, err := normalizeClusterID(in)
			if err != nil {
				t.Errorf("normalizeClusterID(%q) returned err=%v, want nil", in, err)
			}
			if got != in {
				t.Errorf("normalizeClusterID(%q) = %q, want %q", in, got, in)
			}
		}
	})

	t.Run("rejects path traversal and other punctuation", func(t *testing.T) {
		for _, in := range []string{"../bad", "mycluster2/x", "mycluster2.x", "mycluster2 x", "mycluster2;rm", "mycluster2\tx"} {
			if _, err := normalizeClusterID(in); err == nil {
				t.Errorf("normalizeClusterID(%q) returned nil err, want refusal", in)
			}
		}
	})
}

// extractClusterIDs powers the existence-check arm of issue 36. Two
// payload shapes must be supported: the legacy bare-array response and
// the newer single-key envelope conductor adopted for list-style
// endpoints. The test also exercises the malformed-input refusal so a
// surprise server response surfaces as an error rather than a silent
// empty list (which would falsely report every cid as missing).
func TestExtractClusterIDs(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    []string
		wantErr bool
	}{
		{
			name: "bare array with cid field",
			body: `[{"cid":"mycluster2","name":"a"},{"cid":"mycluster3","name":"b"}]`,
			want: []string{"mycluster2", "mycluster3"},
		},
		{
			name: "single-key envelope with cid field",
			body: `{"clusters":[{"cid":"mycluster2"},{"cid":"mycluster3"}]}`,
			want: []string{"mycluster2", "mycluster3"},
		},
		{
			name: "fallback to id field when cid absent",
			body: `{"clusters":[{"id":"mycluster2"}]}`,
			want: []string{"mycluster2"},
		},
		{
			name: "envelope with auxiliary scalar field",
			body: `{"total":2,"clusters":[{"cid":"mycluster2"}]}`,
			want: []string{"mycluster2"},
		},
		{
			name: "empty array",
			body: `[]`,
			want: []string{},
		},
		{
			name:    "malformed json",
			body:    `not json`,
			wantErr: true,
		},
		{
			name:    "envelope with no array",
			body:    `{"meta":{"count":0}}`,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractClusterIDs([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("extractClusterIDs(%q) returned nil err, want refusal", tc.body)
				}
				return
			}
			if err != nil {
				t.Fatalf("extractClusterIDs(%q) returned err=%v, want nil", tc.body, err)
			}
			if !equalClusterIDList(got, tc.want) {
				t.Errorf("extractClusterIDs(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

func equalClusterIDList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Smoke check that the error message names the offending value so a
// user pasting a bad cid sees what was rejected.
func TestNormalizeClusterID_ErrorMentionsValue(t *testing.T) {
	_, err := normalizeClusterID("bad/cid")
	if err == nil {
		t.Fatal("expected refusal")
	}
	if !strings.Contains(err.Error(), "bad/cid") {
		t.Errorf("error %q does not mention rejected value", err)
	}
}
