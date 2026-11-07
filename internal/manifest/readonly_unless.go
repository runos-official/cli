package manifest

import "encoding/json"

// ReadOnlyUnless is conductor's marker for a command that changes nothing
// unless one input field holds one of the listed values (FCR 1909). The
// watchdog probe carries {field: LOAD_DRIVER, values: ["true"]}: by default it
// reads sysfs and /proc only, and LOAD_DRIVER=true loads a kernel module.
//
// Values compare case-insensitively. Any other value, or no value, means the
// call is read-only.
//
// Malformed is true when the JSON did not have the shape {field: string,
// values: [string...]}. A marker this CLI cannot read must never relax a
// confirmation, so the classifier treats Malformed as "always destructive".
// Parsing records the defect instead of failing: one bad marker must not
// make every command in the manifest unusable.
type ReadOnlyUnless struct {
	Field     string   `yaml:"field" json:"field"`
	Values    []string `yaml:"values" json:"values"`
	Malformed bool     `yaml:"-" json:"-"`
}

// UnmarshalJSON accepts only the documented shape and flags anything else as
// Malformed. It never returns an error.
func (r *ReadOnlyUnless) UnmarshalJSON(data []byte) error {
	type rawReadOnlyUnless struct {
		Field  string   `json:"field"`
		Values []string `json:"values"`
	}
	var raw rawReadOnlyUnless
	if err := json.Unmarshal(data, &raw); err != nil {
		*r = ReadOnlyUnless{Malformed: true}
		return nil
	}
	*r = ReadOnlyUnless{Field: raw.Field, Values: raw.Values}
	return nil
}
