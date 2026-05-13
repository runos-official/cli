package jobs

import "testing"

func TestValidateJobID(t *testing.T) {
	cases := []struct {
		name string
		id   string
		want bool
	}{
		{"canonical lowercase", "550e8400-e29b-41d4-a716-446655440000", true},
		{"uppercase", "550E8400-E29B-41D4-A716-446655440000", true},
		{"empty", "", false},
		{"missing dashes", "550e8400e29b41d4a716446655440000", false},
		{"too short", "550e8400-e29b-41d4-a716-44665544000", false},
		{"too long", "550e8400-e29b-41d4-a716-4466554400000", false},
		{"non-hex chars", "550e8400-e29b-41d4-a716-44665544000g", false},
		{"not-a-uuid sentinel", "not-a-uuid", false},
		{"slug-shape", "iter25-baseapp", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ValidateJobID(tc.id)
			if got != tc.want {
				t.Errorf("ValidateJobID(%q) = %v, want %v", tc.id, got, tc.want)
			}
		})
	}
}
