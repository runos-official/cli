package workspace

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// The exit code has to come home without ever appearing on the user's screen, and the output has
// to keep streaming while that happens.

func feed(t *testing.T, chunks []string) (string, *ExitScanner) {
	t.Helper()
	var out bytes.Buffer
	s := NewExitScanner(&out)
	for _, c := range chunks {
		if _, err := s.Write([]byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	return out.String(), s
}

func TestTheExitCodeIsTakenOffTheOutput(t *testing.T) {
	got, s := feed(t, []string{"hello\n" + "\n" + ExitMarker + "3\n"})
	if !s.Found || s.Code != 3 {
		t.Fatalf("expected code 3, got found=%v code=%d", s.Found, s.Code)
	}
	if got != "hello\n" {
		t.Fatalf("the marker must never reach the screen, got %q", got)
	}
}

func TestAMarkerSplitAcrossFramesIsStillFound(t *testing.T) {
	// THE CASE THIS EXISTS FOR. Output arrives in websocket frames with no relationship to lines,
	// so the marker lands half in one frame and half in the next. A scanner that looked at each
	// frame alone would miss it, print it at the user, and report the wrong code.
	whole := "output line\n\n" + ExitMarker + "42\n"
	for cut := 1; cut < len(whole); cut++ {
		got, s := feed(t, []string{whole[:cut], whole[cut:]})
		if !s.Found || s.Code != 42 {
			t.Fatalf("split at %d: expected code 42, got found=%v code=%d", cut, s.Found, s.Code)
		}
		if strings.Contains(got, ExitMarker) {
			t.Fatalf("split at %d: the marker reached the screen: %q", cut, got)
		}
		if got != "output line\n" {
			t.Fatalf("split at %d: output was mangled: %q", cut, got)
		}
	}
}

func TestOutputStreamsRatherThanBuffering(t *testing.T) {
	// A one-shot can be a long build. Holding its output back until it finished would be a worse
	// feature than not having exit codes at all, so only a short tail may be withheld.
	var out bytes.Buffer
	s := NewExitScanner(&out)
	big := strings.Repeat("a line of build output\n", 500)
	if _, err := s.Write([]byte(big)); err != nil {
		t.Fatal(err)
	}
	written := out.Len()
	if written < len(big)-holdBack()-1 {
		t.Fatalf("only %d of %d bytes reached the screen; it is buffering, not streaming", written, len(big))
	}
}

func TestNoMarkerMeansNoInformationAndNothingLost(t *testing.T) {
	// An interactive session and an older far end both send no marker. That must read as "no
	// information", never as success or failure, and NOTHING may be swallowed.
	got, s := feed(t, []string{"just some output with no marker at all"})
	if s.Found {
		t.Fatal("no marker means nothing was reported")
	}
	if s.Code != 0 {
		t.Fatalf("code should stay 0, got %d", s.Code)
	}
	if got != "just some output with no marker at all" {
		t.Fatalf("output was lost or altered: %q", got)
	}
}

func TestATailShorterThanTheMarkerIsStillDelivered(t *testing.T) {
	// The held-back tail is ordinary output when no marker ever comes. Losing it would silently
	// truncate the last line of every command whose output is short.
	got, _ := feed(t, []string{"hi"})
	if got != "hi" {
		t.Fatalf("a short output must survive, got %q", got)
	}
}

func TestEveryExitCodeSurvives(t *testing.T) {
	for _, code := range []int{0, 1, 2, 127, 130, 255} {
		got, s := feed(t, []string{fmt.Sprintf("out\n\n%s%d\n", ExitMarker, code)})
		if !s.Found || s.Code != code {
			t.Fatalf("code %d: got found=%v code=%d", code, s.Found, s.Code)
		}
		if got != "out\n" {
			t.Fatalf("code %d: output was %q", code, got)
		}
	}
}

func TestWhatFollowsTheMarkerIsSwallowed(t *testing.T) {
	// After the marker the shell is exiting, and anything it emits on the way out is noise the
	// caller did not ask for.
	got, s := feed(t, []string{"real output\n\n" + ExitMarker + "0\n", "\x1b]0;some prompt\x07logout\r\n"})
	if !s.Found {
		t.Fatal("the marker should have been found")
	}
	if got != "real output\n" {
		t.Fatalf("trailing noise reached the screen: %q", got)
	}
}
