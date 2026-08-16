package dynacmd

import "testing"

// A description that opens with an example was cut at the abbreviation:
// `Set a label, e.g.` was the whole `Short` on several commands, because
// the full stop in "e.g." is followed by a space like any sentence end.
// RunOS manifest descriptions use "e.g." and "i.e." constantly.
func TestFirstSentence_KeepsGoingPastAnAbbreviation(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Set a label, e.g. env=prod. Then more.", "Set a label, e.g. env=prod."},
		{"Pass the id, i.e. the 5-char osid. Then more.", "Pass the id, i.e. the 5-char osid."},
		{"Ends at the abbreviation e.g.", "Ends at the abbreviation e.g."},
		{"Upper case E.G. still counts. Next.", "Upper case E.G. still counts."},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := firstSentence(c.in); got != c.want {
				t.Errorf("firstSentence(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
