package output

import "testing"

func TestCursorArgumentPreservesShellCharacters(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ cursor, want string }{
		{"opaque cursor/value", "'opaque cursor/value'"},
		{"cursor'quote", "'cursor'\"'\"'quote'"},
		{"cursor$variable`literal`", "'cursor$variable`literal`'"},
	} {
		if got := shellArgument(test.cursor); got != test.want {
			t.Errorf("cursor %q: got %q, want %q", test.cursor, got, test.want)
		}
	}
}
