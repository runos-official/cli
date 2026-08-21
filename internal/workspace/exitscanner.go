package workspace

import (
	"io"
	"strconv"
	"strings"
)

// ExitScanner passes a one-shot command's output through to the terminal while watching for the
// line that carries its exit code, and keeps that line off the screen.
//
// IT STREAMS RATHER THAN BUFFERS. A one-shot can be a long build or a log tail, and holding its
// output back until it finished would be a worse feature than not having exit codes at all. So it
// writes through and holds back only a short tail, which is the smallest amount that can still
// contain a marker split across two frames.
//
// A MARKER SPLIT ACROSS FRAMES IS THE CASE THIS EXISTS FOR. Output arrives in websocket frames
// with no relationship to lines, so the marker can and does land half in one and half in the next.
// Scanning each frame on its own would miss it, print it to the user, and report the wrong exit
// code.
type ExitScanner struct {
	out  io.Writer
	tail []byte

	// Code is the exit code the far end reported. Zero until Found is true.
	Code int
	// Found says whether the marker was seen. An older far end, or an interactive session, never
	// sends one, and that must read as "no information" rather than as success or failure.
	Found bool
}

func NewExitScanner(out io.Writer) *ExitScanner {
	return &ExitScanner{out: out}
}

// holdBack is how much of the stream is kept unwritten while waiting to see whether it turns into
// a marker. The marker plus the longest exit code plus the newlines around it.
func holdBack() int { return len(ExitMarker) + 16 }

func (s *ExitScanner) Write(p []byte) (int, error) {
	if s.Found {
		// Everything after the marker is the shell exiting. Swallow it rather than print it.
		return len(p), nil
	}
	s.tail = append(s.tail, p...)

	if idx := strings.LastIndex(string(s.tail), ExitMarker); idx >= 0 {
		rest := string(s.tail[idx+len(ExitMarker):])
		end := strings.IndexAny(rest, "\r\n")
		if end < 0 {
			// The code has not fully arrived yet. Wait for the rest rather than parsing half of it.
			return len(p), nil
		}
		if code, err := strconv.Atoi(strings.TrimSpace(rest[:end])); err == nil {
			s.Code = code
			s.Found = true
		}
		// Write what came before the marker, minus the newline the marker was printed after.
		before := s.tail[:idx]
		before = trimOneNewline(before)
		s.tail = nil
		if len(before) > 0 {
			if _, err := s.out.Write(before); err != nil {
				return 0, err
			}
		}
		return len(p), nil
	}

	// No marker yet. Write everything except a tail short enough to be the start of one.
	if len(s.tail) > holdBack() {
		cut := len(s.tail) - holdBack()
		if _, err := s.out.Write(s.tail[:cut]); err != nil {
			return 0, err
		}
		s.tail = append([]byte(nil), s.tail[cut:]...)
	}
	return len(p), nil
}

// Flush writes whatever is still held back. Called once the session has ended, because that tail is
// ordinary output when no marker ever came.
func (s *ExitScanner) Flush() error {
	if s.Found || len(s.tail) == 0 {
		return nil
	}
	_, err := s.out.Write(s.tail)
	s.tail = nil
	return err
}

func trimOneNewline(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
		if n = len(b); n > 0 && b[n-1] == '\r' {
			b = b[:n-1]
		}
	}
	return b
}
