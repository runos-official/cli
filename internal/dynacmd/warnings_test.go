package dynacmd

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

// Objective 92 / story 211. advisoryWarnings is the whole shape contract
// the generic renderer keys on, so every malformed input it can be handed
// by a conductor that has not shipped yet is pinned here. It must be
// total: no case may panic, and no case that did not print may report
// itself complete.
func TestAdvisoryWarnings(t *testing.T) {
	cases := []struct {
		name         string
		body         string
		wantLines    []string
		wantComplete bool
	}{
		{
			name:         "two string entries in body order",
			body:         `{"id":"svc1","warnings":["first advisory","second advisory"]}`,
			wantLines:    []string{"first advisory", "second advisory"},
			wantComplete: true,
		},
		{
			name:         "single entry",
			body:         `{"warnings":["only one"]}`,
			wantLines:    []string{"only one"},
			wantComplete: true,
		},
		{
			name: "missing key",
			body: `{"id":"svc1"}`,
		},
		{
			name: "empty array",
			body: `{"id":"svc1","warnings":[]}`,
		},
		{
			name: "null value",
			body: `{"id":"svc1","warnings":null}`,
		},
		{
			// The singular key is the api-keys / notify-keys banner's.
			// A string under the PLURAL key is still not an array, so it
			// is not ours either.
			name: "non-array value",
			body: `{"id":"svc1","warnings":"not an array"}`,
		},
		{
			name: "array of objects prints nothing rather than map[...]",
			body: `{"id":"svc1","warnings":[{"text":"structured"}]}`,
		},
		{
			// Mixed array: the string entries print, but complete is
			// false so the caller keeps the key and the entry that
			// printed no line is still visible in the table.
			name:         "mixed entries print the strings and report incomplete",
			body:         `{"id":"svc1","warnings":["a string",{"text":"structured"},"another string"]}`,
			wantLines:    []string{"a string", "another string"},
			wantComplete: false,
		},
		{
			name: "numbers are not strings",
			body: `{"warnings":[1,2]}`,
		},
		{
			name: "body is an array, not an object",
			body: `[{"warnings":["nope"]}]`,
		},
		{
			name: "body is not JSON at all",
			body: `not json`,
		},
		{
			name: "empty body",
			body: ``,
		},
		{
			name: "whitespace only",
			body: "   \n\t ",
		},
		{
			name:         "leading whitespace does not hide the object",
			body:         "\n  {\"warnings\":[\"still found\"]}",
			wantLines:    []string{"still found"},
			wantComplete: true,
		},
		{
			// Case matters: the key is `warnings`, exactly.
			name: "differently cased key is not the advisory key",
			body: `{"Warnings":["nope"]}`,
		},
		{
			name: "singular key is never read",
			body: `{"warning":"the one-shot token banner owns this"}`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lines, complete := advisoryWarnings([]byte(c.body))
			if !reflect.DeepEqual(lines, c.wantLines) {
				t.Errorf("lines = %#v, want %#v", lines, c.wantLines)
			}
			if complete != c.wantComplete {
				t.Errorf("complete = %v, want %v", complete, c.wantComplete)
			}
		})
	}
}

// stripAdvisoryWarnings must remove the advisory key and nothing else,
// and must hand back the original bytes untouched for every shape it
// does not recognise.
func TestStripAdvisoryWarnings(t *testing.T) {
	t.Run("removes only the warnings key", func(t *testing.T) {
		in := []byte(`{"jobId":"job-1","osid":"osid-1","warnings":["advisory"]}`)
		got := stripAdvisoryWarnings(in)
		var decoded map[string]any
		if err := json.Unmarshal(got, &decoded); err != nil {
			t.Fatalf("stripped body must still be JSON: %v (%s)", err, got)
		}
		if _, present := decoded["warnings"]; present {
			t.Errorf("warnings key survived the strip: %s", got)
		}
		if decoded["jobId"] != "job-1" || decoded["osid"] != "osid-1" {
			t.Errorf("strip changed a sibling value: %s", got)
		}
		if len(decoded) != 2 {
			t.Errorf("strip changed the key count: %s", got)
		}
	})

	t.Run("nested warnings keys are untouched", func(t *testing.T) {
		in := []byte(`{"result":{"warnings":["nested stays"]},"warnings":["top goes"]}`)
		got := stripAdvisoryWarnings(in)
		var decoded map[string]any
		if err := json.Unmarshal(got, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, present := decoded["warnings"]; present {
			t.Errorf("top-level warnings survived: %s", got)
		}
		result, ok := decoded["result"].(map[string]any)
		if !ok {
			t.Fatalf("result block lost: %s", got)
		}
		nested, ok := result["warnings"].([]any)
		if !ok || len(nested) != 1 || nested[0] != "nested stays" {
			t.Errorf("nested warnings changed: %s", got)
		}
	})

	t.Run("unrecognised shapes come back byte-identical", func(t *testing.T) {
		for _, body := range []string{
			`{"id":"svc1"}`,
			`[1,2,3]`,
			`not json`,
			``,
			`{"warning":"singular"}`,
		} {
			in := []byte(body)
			if got := stripAdvisoryWarnings(in); !bytes.Equal(got, in) {
				t.Errorf("stripAdvisoryWarnings(%q) = %q, want the input unchanged", body, got)
			}
		}
	})
}

// The printed line is copied from `runos deploy` (cmd/deploy.go), text
// and all, so an operator sees one shape whichever command they ran.
func TestPrintAdvisoryWarnings(t *testing.T) {
	var buf bytes.Buffer
	printAdvisoryWarnings(&buf, []string{"first", "second"})
	want := "Warning: first\nWarning: second\n"
	if buf.String() != want {
		t.Errorf("output = %q, want %q", buf.String(), want)
	}

	buf.Reset()
	printAdvisoryWarnings(&buf, nil)
	if buf.Len() != 0 {
		t.Errorf("no advisories must write nothing, got %q", buf.String())
	}
}
