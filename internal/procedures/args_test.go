package procedures

import (
	"strings"
	"testing"
)

func specs() []ArgSpec {
	return []ArgSpec{
		{Name: "osid", Type: "string", Required: true, Description: "the service"},
		{Name: "database", Type: "integer", Description: "which database"},
		{Name: "confirm", Type: "boolean", Description: "acknowledge the deletion"},
		{Name: "mode", Type: "enum", Values: []string{"keys", "all"}, Description: "what to flush"},
	}
}

// The requirement: Conductor performs NO coercion, because an argument
// reaches a plan hash, a lock key and an approval render, so a path that
// accepts one spelling and canonicalises it to another is a path where
// the approved and requested plans can differ. The terminal has only
// strings, so the CLI converts against the server's declared types.
func TestCoerceArgsProducesTheDeclaredJSONTypes(t *testing.T) {
	values, err := CoerceArgs(specs(), []string{
		"osid=valkey-ab2cd", "database=3", "confirm=true", "mode=keys",
	})
	if err != nil {
		t.Fatalf("CoerceArgs: %v", err)
	}
	if got, ok := values["osid"].(string); !ok || got != "valkey-ab2cd" {
		t.Fatalf("osid = %#v, want the string", values["osid"])
	}
	// The assertion that matters: an integer argument must not be sent as
	// the string "3", which conductor rejects.
	if got, ok := values["database"].(int64); !ok || got != 3 {
		t.Fatalf("database = %#v, want int64(3)", values["database"])
	}
	if got, ok := values["confirm"].(bool); !ok || got != true {
		t.Fatalf("confirm = %#v, want bool(true)", values["confirm"])
	}
	if got, ok := values["mode"].(string); !ok || got != "keys" {
		t.Fatalf("mode = %#v, want the enum string", values["mode"])
	}
}

func TestCoerceArgsRefusesRatherThanGuesses(t *testing.T) {
	cases := []struct {
		name  string
		pairs []string
		want  string
	}{
		{"unknown argument", []string{"osid=x", "nope=1"}, `unknown argument "nope"`},
		{"missing required", []string{"database=1"}, `missing required argument "osid"`},
		{"non-integer for integer", []string{"osid=x", "database=three"}, `must be an integer`},
		{"non-boolean for boolean", []string{"osid=x", "confirm=yes"}, `must be true or false`},
		{"value outside the enum", []string{"osid=x", "mode=everything"}, `not one of: keys, all`},
		{"no equals sign", []string{"osid"}, `is not name=value`},
		{"repeated name", []string{"osid=a", "osid=b"}, `was given twice`},
		{"unsupported declared type", []string{"weird=1"}, `unknown argument "weird"`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := CoerceArgs(specs(), testCase.pairs)
			if err == nil {
				t.Fatalf("expected a refusal for %v", testCase.pairs)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), testCase.want)
			}
		})
	}
}

// A boolean that only accepts true and false, deliberately narrower than
// strconv.ParseBool: a Procedure argument that deletes something should
// not be settable by a character whose meaning depends on knowing Go's
// parser.
func TestCoerceArgsRefusesParseBoolShorthands(t *testing.T) {
	for _, shorthand := range []string{"t", "f", "1", "0", "TRUE!", "y", "n"} {
		if _, err := CoerceArgs(specs(), []string{"osid=x", "confirm=" + shorthand}); err == nil {
			t.Errorf("confirm=%q was accepted; only true and false are boolean here", shorthand)
		}
	}
	// Case and surrounding space are ordinary user input, not shorthand.
	for _, accepted := range []string{"TRUE", " true ", "False"} {
		if _, err := CoerceArgs(specs(), []string{"osid=x", "confirm=" + accepted}); err != nil {
			t.Errorf("confirm=%q was refused: %v", accepted, err)
		}
	}
}

// Every problem at once, mirroring conductor's own "every failing
// reason, not the first". A user who fixes one gap and resubmits into
// the next learns the shape of the command one round trip at a time.
func TestCoerceArgsReportsEveryProblemAtOnce(t *testing.T) {
	_, err := CoerceArgs(specs(), []string{"nope=1", "database=three", "mode=everything"})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	message := err.Error()
	for _, expected := range []string{`unknown argument "nope"`, "must be an integer", "not one of", `missing required argument "osid"`} {
		if !strings.Contains(message, expected) {
			t.Errorf("the combined error is missing %q:\n%s", expected, message)
		}
	}
	if !strings.Contains(message, "4 argument problem(s)") {
		t.Errorf("the count is wrong:\n%s", message)
	}
}

// An empty argument list against a spec with no required fields is a
// legitimate call, not an error.
func TestCoerceArgsAcceptsNoArgumentsWhenNoneAreRequired(t *testing.T) {
	values, err := CoerceArgs([]ArgSpec{{Name: "optional", Type: "string"}}, nil)
	if err != nil {
		t.Fatalf("CoerceArgs: %v", err)
	}
	if len(values) != 0 {
		t.Fatalf("values = %#v, want empty", values)
	}
}

// A value containing '=' belongs to the value, not to a second split.
func TestCoerceArgsSplitsOnTheFirstEqualsOnly(t *testing.T) {
	values, err := CoerceArgs([]ArgSpec{{Name: "pattern", Type: "string", Required: true}}, []string{"pattern=a=b=c"})
	if err != nil {
		t.Fatalf("CoerceArgs: %v", err)
	}
	if values["pattern"] != "a=b=c" {
		t.Fatalf("pattern = %#v, want the whole value", values["pattern"])
	}
}

// A declared type this CLI does not know must refuse rather than send an
// untyped guess: the CLI being older than the Procedure is a real state.
func TestCoerceArgsRefusesAnUnknownDeclaredType(t *testing.T) {
	_, err := CoerceArgs([]ArgSpec{{Name: "future", Type: "duration", Required: true}}, []string{"future=5m"})
	if err == nil {
		t.Fatal("expected a refusal for an unsupported declared type")
	}
	if !strings.Contains(err.Error(), "runos update") {
		t.Fatalf("the error does not name the recovery path: %v", err)
	}
}
