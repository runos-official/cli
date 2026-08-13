package dynacmd

import (
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

func TestDescribeConstraints(t *testing.T) {
	tests := []struct {
		name  string
		field manifest.Field
		want  string
	}{
		{
			// The exact field from the finding. Before this, the allowed values could only be
			// learned by sending a wrong one and reading the refusal.
			name: "the enum the finding was raised for",
			field: manifest.Field{
				Name: "format",
				Enum: []string{"ssh-local", "ssh-remote", "aws-cli"},
			},
			want: " (one of: ssh-local, ssh-remote, aws-cli)",
		},
		{
			name:  "no constraints renders nothing, so an ordinary flag is unchanged",
			field: manifest.Field{Name: "name", Type: "string"},
			want:  "",
		},
		{
			name:  "format alone, because a named shape is what a caller gets wrong",
			field: manifest.Field{Name: "tags", Format: "key_value"},
			want:  " (format: key_value)",
		},
		{
			name: "enum and format together",
			field: manifest.Field{
				Name:   "when",
				Enum:   []string{"now", "later"},
				Format: "duration",
			},
			want: " (one of: now, later; format: duration)",
		},
		{
			name:  "a meaningful default is shown",
			field: manifest.Field{Name: "profile", Default: "local-pinned"},
			want:  " (default: local-pinned)",
		},
		{
			// Cobra already prints its own "(default ...)" for non-zero values, and repeating
			// "default: false" on every untouched flag pushes the real constraint off the line.
			name:  "a zero-valued default is not shown",
			field: manifest.Field{Name: "force", Default: false},
			want:  "",
		},
		{
			name:  "an empty-string default is not shown",
			field: manifest.Field{Name: "note", Default: ""},
			want:  "",
		},
		{
			name:  "a zero integer default is not shown",
			field: manifest.Field{Name: "count", Default: float64(0)},
			want:  "",
		},
		{
			// Manifest numbers arrive as float64 through JSON; "default: 3" not "default: 3.0".
			name:  "a whole-number default has no trailing decimal",
			field: manifest.Field{Name: "replicas", Default: float64(3)},
			want:  " (default: 3)",
		},
		{
			name:  "a true boolean default IS shown, because it is not the zero value",
			field: manifest.Field{Name: "autoRestart", Default: true},
			want:  " (default: true)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := describeConstraints(tt.field); got != tt.want {
				t.Errorf("describeConstraints() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDescribeConstraintsLongEnum(t *testing.T) {
	// A very long list turns a one-line flag description into a wall. Truncate, but report the
	// real count so the reader knows the list is cut rather than short.
	values := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}
	got := describeConstraints(manifest.Field{Name: "size", Enum: values})

	if !strings.Contains(got, "one of 11") {
		t.Errorf("expected the real count in %q", got)
	}
	if strings.Contains(got, "k") && strings.Contains(got, ", k") {
		t.Errorf("expected the tail to be truncated, got %q", got)
	}
	if !strings.Contains(got, "a, b, c") {
		t.Errorf("expected the first values to still be listed, got %q", got)
	}
}

func TestDescribeConstraintsIsAppendable(t *testing.T) {
	// Callers append unconditionally, so a present suffix must carry its own leading space and
	// an absent one must contribute nothing at all.
	base := "Command format for the target environment"

	withEnum := base + describeConstraints(manifest.Field{Enum: []string{"x", "y"}})
	if withEnum != base+" (one of: x, y)" {
		t.Errorf("unexpected joined description: %q", withEnum)
	}

	withNothing := base + describeConstraints(manifest.Field{})
	if withNothing != base {
		t.Errorf("an unconstrained field must not change the description, got %q", withNothing)
	}
}

func TestRequiredBooleanNamesItsFalseForm(t *testing.T) {
	// O21: cobra renders a bool as a bare `--flag` with no `=value` hint, so the false case
	// looked impossible. On nodes/configure-virt-shape that is half the command's purpose.
	required := describeConstraints(manifest.Field{
		Name:     "vmHost",
		Type:     "boolean",
		Required: true,
	})
	if !strings.Contains(required, "=false") {
		t.Errorf("a required boolean must name its false form, got %q", required)
	}

	// An optional bool defaults sensibly; the hint would just be noise on every one of them.
	optional := describeConstraints(manifest.Field{Name: "force", Type: "boolean"})
	if strings.Contains(optional, "=false") {
		t.Errorf("an optional boolean should not carry the hint, got %q", optional)
	}

	// A required NON-boolean is unaffected.
	str := describeConstraints(manifest.Field{Name: "name", Type: "string", Required: true})
	if str != "" {
		t.Errorf("a required string should be unchanged, got %q", str)
	}
}
