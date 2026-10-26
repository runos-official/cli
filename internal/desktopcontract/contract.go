// Package desktopcontract checks the JSON the CLI prints for RunOS Desktop against a documented
// table. Desktop decodes this output with strict Swift types, and one wrong shape (a null where it
// reads a list) makes it drop the whole answer and show a bare menu with no error. Tests encode the
// real Go output types, from a zero value and from a fully populated one, and run the bytes through
// Check, so a new field has to be added to the table and a null cannot reach the wire unnoticed.
//
// Only tests import this package, so it is not linked into the runos binary.
package desktopcontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Kind is the JSON type a field must have.
type Kind string

const (
	// String is a JSON string. It may be empty.
	String Kind = "string"
	// Int is a JSON number with no fraction and no exponent.
	Int Kind = "int"
	// Bool is true or false, never a string or a number.
	Bool Kind = "bool"
	// Time is an RFC 3339 UTC timestamp in whole seconds, never the year-1 zero time.
	Time Kind = "time"
	// Strings is an array of strings. It may be empty and is never null.
	Strings Kind = "strings"
	// Object is a nested object. Its keys are listed as "path.key".
	Object Kind = "object"
	// Objects is an array of objects. Its keys are listed as "path[].key".
	Objects Kind = "objects"
)

// Field is one row of the contract: a key path, its type, and whether the key must be present.
// No field is ever allowed to be null, so there is no nullable column.
type Field struct {
	Path     string
	Kind     Kind
	Required bool
	// Desktop says how RunOS Desktop reads the field: DesktopRequired (a non-optional Swift
	// property, so a missing or null value fails the whole decode), DesktopOptional, or DesktopIgnored.
	// Ignored rows are kept so every key the CLI sends is accounted for, and so a new key cannot
	// appear without a decision.
	Desktop Reads
}

// Reads is how Desktop treats one field.
type Reads string

const (
	// DesktopRequired is a non-optional Swift property. These are the dangerous ones.
	DesktopRequired Reads = "required"
	// DesktopOptional is an optional Swift property: absent is fine, wrong type is not.
	DesktopOptional Reads = "optional"
	// DesktopIgnored is a key Desktop does not decode.
	DesktopIgnored Reads = "ignored"
)

// Contract is the full table for one command's output.
type Contract []Field

// Check fails t once for every violation found in one JSON document.
func Check(t interface {
	Helper()
	Errorf(string, ...any)
}, name string, doc []byte, c Contract) {
	t.Helper()
	for _, v := range Violations(doc, c) {
		t.Errorf("%s: %s", name, v)
	}
}

// Violations lists every way doc breaks the contract. It is empty when doc conforms.
func Violations(doc []byte, c Contract) []string {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return []string{"not valid JSON: " + err.Error()}
	}
	object, ok := root.(map[string]any)
	if !ok {
		return []string{fmt.Sprintf("the document is %T, want a JSON object", root)}
	}
	byPath := make(map[string]Field, len(c))
	for _, f := range c {
		byPath[f.Path] = f
	}
	var out []string
	walk(object, "", c, byPath, &out)
	sort.Strings(out)
	return out
}

func walk(object map[string]any, prefix string, c Contract, byPath map[string]Field, out *[]string) {
	for _, f := range c {
		if !f.Required || !strings.HasPrefix(f.Path, prefix) {
			continue
		}
		rest := strings.TrimPrefix(f.Path, prefix)
		if strings.Contains(rest, ".") {
			continue
		}
		if _, present := object[rest]; !present {
			*out = append(*out, fmt.Sprintf("required key %s is missing", f.Path))
		}
	}
	for key, value := range object {
		path := prefix + key
		f, known := byPath[path]
		switch {
		case !known:
			*out = append(*out, fmt.Sprintf("key %s is not in the contract table: document it, or remove it", path))
		case value == nil:
			*out = append(*out, fmt.Sprintf("%s is null: Desktop cannot read null; send [], {} or omit the key", path))
		default:
			checkValue(path, f, value, c, byPath, out)
		}
	}
}

func checkValue(path string, f Field, value any, c Contract, byPath map[string]Field, out *[]string) {
	bad := func(format string, args ...any) {
		*out = append(*out, path+": "+fmt.Sprintf(format, args...))
	}
	switch f.Kind {
	case String:
		if _, ok := value.(string); !ok {
			bad("want a string, got %T", value)
		}
	case Bool:
		if _, ok := value.(bool); !ok {
			bad("want a bool, got %T", value)
		}
	case Int:
		n, ok := value.(json.Number)
		if !ok {
			bad("want an integer, got %T", value)
		} else if _, err := strconv.ParseInt(n.String(), 10, 64); err != nil {
			bad("want an integer, got %s", n)
		}
	case Time:
		s, ok := value.(string)
		if !ok {
			bad("want an RFC 3339 string, got %T", value)
			return
		}
		if msg := badTimestamp(s); msg != "" {
			bad("%s", msg)
		}
	case Strings:
		list, ok := value.([]any)
		if !ok {
			bad("want an array of strings, got %T", value)
			return
		}
		for i, item := range list {
			if _, ok := item.(string); !ok {
				bad("element %d is %T, want a string", i, item)
			}
		}
	case Object:
		child, ok := value.(map[string]any)
		if !ok {
			bad("want an object, got %T", value)
			return
		}
		walk(child, path+".", c, byPath, out)
	case Objects:
		list, ok := value.([]any)
		if !ok {
			bad("want an array of objects, got %T", value)
			return
		}
		for i, item := range list {
			child, ok := item.(map[string]any)
			if !ok {
				bad("element %d is %T, want an object", i, item)
				continue
			}
			walk(child, path+"[].", c, byPath, out)
		}
	default:
		bad("the contract table gives the unknown kind %q", f.Kind)
	}
}

// badTimestamp explains why s is not a timestamp Desktop can read on every macOS version it
// supports, or returns "" when it is. Whole seconds and a literal Z keep it independent of how the
// system's ISO 8601 parser treats fractions and offsets; a year before 2000 is the zero time Go
// writes for an unset field, which means "never" and must be omitted instead.
func badTimestamp(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return fmt.Sprintf("%q is not RFC 3339", s)
	}
	if strings.Contains(s, ".") {
		return fmt.Sprintf("%q has fractional seconds; send whole seconds", s)
	}
	if !strings.HasSuffix(s, "Z") {
		return fmt.Sprintf("%q is not in UTC (it must end in Z)", s)
	}
	if t.Year() < 2000 {
		return fmt.Sprintf("%q is the zero time; omit an unset timestamp", s)
	}
	return ""
}
