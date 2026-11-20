package api

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// thirdPartyMarker is written on (or just above) an http.Client that talks to a host that is not
// conductor, so the CLI version is not sent there.
const thirdPartyMarker = "no CLI user agent"

/*
Every http.Client in the CLI either carries api.NewTransport() or says why not.

A client built without it sends no runos-cli User-Agent, and conductor's minimum-version gate counts
a request without one as a CLI from before the gate. It also shows a 426 as a raw HTTP body
instead of the plain update message. The scan fails the build the day someone adds a conductor
client and forgets the transport.
*/
func TestEveryHTTPClientIsStampedOrMarkedThirdParty(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(raw), "\n")
		for i, line := range lines {
			if !strings.Contains(line, "http.Client{") || strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			// The literal's own braces, so a neighbouring client's transport cannot vouch for it,
			// plus the line before and the rest of the line it ends on, for the marker.
			window := lines[max(0, i-1)] + "\n" + literalSpan(lines, i)
			if !strings.Contains(window, "NewTransport()") && !strings.Contains(window, thirdPartyMarker) {
				t.Errorf("%s:%d builds an http.Client with neither api.NewTransport() nor the %q marker", path, i+1, thirdPartyMarker)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// literalSpan is the text from the line holding "http.Client{" to the end of the line where that
// literal's braces close.
func literalSpan(lines []string, start int) string {
	depth, started := 0, false
	var span []string
	for i := start; i < len(lines); i++ {
		span = append(span, lines[i])
		text := lines[i]
		if i == start {
			text = text[strings.Index(text, "http.Client{"):]
		}
		for _, r := range text {
			switch r {
			case '{':
				depth++
				started = true
			case '}':
				depth--
			}
		}
		if started && depth <= 0 {
			break
		}
	}
	return strings.Join(span, "\n")
}
