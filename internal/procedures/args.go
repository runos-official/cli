package procedures

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// CoerceArgs converts `--arg name=value` pairs from the terminal into
// the JSON types the Procedure's declared spec requires.
//
// WHY THIS EXISTS AT ALL. Conductor performs no coercion, deliberately:
// `"3"` is not 3, because an argument reaches a plan hash, a lock key
// and an approval render, and a path that quietly accepts one spelling
// and canonicalises it to another is a path where the approved plan and
// the requested plan can differ while both look right. A terminal has
// only strings, so somebody has to convert, and doing it HERE against
// the server's own declared spec means the refusal for a bad value is
// local and specific instead of a round trip that says "must be an
// integer" about a value the user cannot see.
//
// It refuses rather than guesses in every ambiguous case: an unknown
// argument name, a missing required argument, a non-integer for an
// integer, a non-boolean for a boolean, and a value outside an enum. The
// server rejects all of these too; the point is that the user reads the
// reason before anything is sent.
//
// A repeated name is an error rather than a last-one-wins: a user who
// typed a name twice meant one of the two and the CLI cannot know which.
func CoerceArgs(specs []ArgSpec, pairs []string) (map[string]any, error) {
	byName := make(map[string]ArgSpec, len(specs))
	for _, spec := range specs {
		byName[spec.Name] = spec
	}

	out := make(map[string]any, len(pairs))
	var problems []string

	for _, pair := range pairs {
		name, raw, found := strings.Cut(pair, "=")
		if !found {
			problems = append(problems, fmt.Sprintf("%q is not name=value", pair))
			continue
		}
		name = strings.TrimSpace(name)
		spec, declared := byName[name]
		if !declared {
			problems = append(problems, fmt.Sprintf("unknown argument %q (declared: %s)", name, declaredNames(specs)))
			continue
		}
		if _, repeated := out[name]; repeated {
			problems = append(problems, fmt.Sprintf("argument %q was given twice", name))
			continue
		}
		value, err := coerceOne(spec, raw)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		out[name] = value
	}

	// Reported before the wire so the user fixes every gap in one go,
	// mirroring conductor's own "every failing reason, not the first".
	for _, spec := range specs {
		if !spec.Required {
			continue
		}
		if _, present := out[spec.Name]; !present {
			problems = append(problems, fmt.Sprintf("missing required argument %q (%s)", spec.Name, spec.Description))
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("%d argument problem(s):\n  - %s", len(problems), strings.Join(problems, "\n  - "))
	}
	return out, nil
}

func coerceOne(spec ArgSpec, raw string) (any, error) {
	switch spec.Type {
	case "string":
		return raw, nil
	case "enum":
		if slices.Contains(spec.Values, raw) {
			return raw, nil
		}
		return nil, fmt.Errorf("argument %q is %q, which is not one of: %s", spec.Name, raw, strings.Join(spec.Values, ", "))
	case "integer":
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("argument %q must be an integer, got %q", spec.Name, raw)
		}
		return n, nil
	case "boolean":
		// An explicit two-value set rather than strconv.ParseBool, which
		// also accepts "t", "f", "1" and "0". A Procedure argument that
		// deletes something should not be settable by a character whose
		// meaning depends on knowing Go's parser.
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
		return nil, fmt.Errorf("argument %q must be true or false, got %q", spec.Name, raw)
	default:
		return nil, fmt.Errorf("argument %q has an unsupported declared type %q; this CLI is older than the Procedure. Update with 'runos update'", spec.Name, spec.Type)
	}
}

func declaredNames(specs []ArgSpec) string {
	if len(specs) == 0 {
		return "none"
	}
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		if spec.Required {
			names = append(names, spec.Name+" (required)")
			continue
		}
		names = append(names, spec.Name)
	}
	return strings.Join(names, ", ")
}
