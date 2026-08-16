package manifest

import (
	"testing"
	"time"
)

// The manifest drift check runs after a command has ALREADY failed, and
// it costs one more round trip. At the ordinary 10 s deadline an
// unreachable API added ten seconds to every failed command, so the
// advisory probe gets its own shorter one.
func TestLoaderTimeouts(t *testing.T) {
	if AdvisoryTimeout >= DefaultTimeout {
		t.Fatalf("the advisory probe must give up sooner than an ordinary fetch: advisory %v, default %v", AdvisoryTimeout, DefaultTimeout)
	}
	cases := []struct {
		name   string
		loader *Loader
		want   time.Duration
	}{
		{"default", NewLoader("https://api.example", t.TempDir()), DefaultTimeout},
		{"advisory", NewLoaderWithTimeout("https://api.example", t.TempDir(), AdvisoryTimeout), AdvisoryTimeout},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.loader.httpClient.Timeout != c.want {
				t.Errorf("timeout = %v, want %v", c.loader.httpClient.Timeout, c.want)
			}
		})
	}
}
