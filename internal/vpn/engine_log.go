package vpn

import (
	"fmt"
	"sync"
	"time"
)

// wireguardLogInterval is the least time between two identical engine log lines.
var wireguardLogInterval = time.Minute

// maxLimiterKeys bounds the limiter's memory: a message that carries a counter or a timestamp is
// different every time, and would otherwise add a key per line for as long as the daemon runs.
const maxLimiterKeys = 256

/*
rateLimitedLogf wraps the engine's error logger so one persistent fault cannot flood the file, and so
nothing credential-shaped reaches it.

wireguard-go retries a failing handshake every few seconds and logs each failure. A server that
cannot be reached would write thousands of identical lines a day, burying the step lines that matter
and filling a file the daemon is trying to keep small. The first occurrence of a message is written;
repeats inside the interval are counted, and the count is written with the next one.

THE LIMIT IS ON THE FORMATTED MESSAGE, not the format string: the engine uses one format for every
peer, so limiting on it would let one failing peer hide another's. And the message is redacted here,
because the engine's text is not ours: an error it formats can echo a configuration line.
*/
func rateLimitedLogf(next func(format string, args ...any)) func(format string, args ...any) {
	var mu sync.Mutex
	last := map[string]time.Time{}
	dropped := map[string]int{}
	return func(format string, args ...any) {
		msg := redactText(fmt.Sprintf(format, args...))
		mu.Lock()
		now := time.Now()
		if t, ok := last[msg]; ok && now.Sub(t) < wireguardLogInterval {
			dropped[msg]++
			mu.Unlock()
			return
		}
		if len(last) >= maxLimiterKeys {
			last, dropped = map[string]time.Time{}, map[string]int{}
		}
		last[msg] = now
		n := dropped[msg]
		dropped[msg] = 0
		mu.Unlock()
		if n > 0 {
			next("(%d similar line(s) suppressed in the last %s)", n, wireguardLogInterval)
		}
		next("%s", msg)
	}
}
