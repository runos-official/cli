//go:build probe && darwin

package vpn

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

/*
The opt-in probe for FCR 1730, hypothesis H1: does the way a resolver file is written change how long
macOS takes to list the zone?

It is NOT part of `go test`. It needs root, because it writes the real /etc/resolver, so it only runs
through `make dnsprobe`, which builds it as you and runs the binary under sudo. It:

  - uses scratch zones under example.test and the documentation address 192.0.2.53, never a real zone;
  - writes each zone with the OLD method (open with truncate, then write) and the NEW one (the
    production atomic rename), interleaved so a slow minute does not favour either;
  - polls `scutil --dns` until the zone is listed and records how long that took;
  - if a zone is still unlisted after a while, re-publishes it once (the H2 nudge) and records whether
    that rescued it;
  - removes every scratch file it made, and only a file that carries the RunOS marker. It refuses to
    touch any other file, and any zone that is not a scratch zone.

Settings (environment): RUNOS_DNS_PROBE=1 (required), RUNOS_DNS_PROBE_N runs per method (default 10).
*/

const (
	probeZonePrefix = "dnsprobe-"
	probeZoneSuffix = ".example.test"
	probeNameserver = "192.0.2.53"
)

type probeEnv struct {
	dir     string
	scutil  func() (string, error)
	flush   func() error
	now     func() time.Time
	sleep   func(time.Duration)
	out     io.Writer
	n       int
	runID   string
	poll    time.Duration
	giveUp  time.Duration
	nudgeAt time.Duration
	// stopped reports that the operator interrupted the run.
	stopped func() bool
	made    []string
}

type probeResult struct {
	method      string
	iteration   int
	latency     time.Duration
	listed      bool
	republished bool
}

type probeMethod struct {
	name  string
	write func(env *probeEnv, zone string) error
}

func probeMethods() []probeMethod {
	content := resolverMarker + "\nnameserver " + probeNameserver + "\n"
	return []probeMethod{
		{"truncate+write (old)", func(env *probeEnv, zone string) error {
			return os.WriteFile(filepath.Join(env.dir, zone), []byte(content), 0o644)
		}},
		{"atomic rename (new)", func(env *probeEnv, zone string) error {
			return env.platform().SetResolver(zone, netip.MustParseAddr(probeNameserver))
		}},
	}
}

// scratchZone is the only kind of zone the probe touches.
func scratchZone(runID string, method, i int) string {
	return fmt.Sprintf("%s%s-%d-%d%s", probeZonePrefix, runID, method, i, probeZoneSuffix)
}

func isScratchZone(zone string) bool {
	return strings.HasPrefix(zone, probeZonePrefix) && strings.HasSuffix(zone, probeZoneSuffix) &&
		!strings.ContainsAny(zone, "/\\")
}

// guard refuses to write over, or remove, anything that is not a scratch zone or not ours.
func (env *probeEnv) guard(zone string) error {
	if !isScratchZone(zone) {
		return fmt.Errorf("refusing to touch %q: not a scratch zone", zone)
	}
	data, err := os.ReadFile(filepath.Join(env.dir, zone))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !isManagedResolverFile(data) {
		return fmt.Errorf("refusing to touch %q: the file does not carry the RunOS marker", zone)
	}
	return nil
}

func (env *probeEnv) platform() darwinPlatform { return darwinPlatform{dir: env.dir} }

func (env *probeEnv) remove(zone string) {
	if env.guard(zone) != nil {
		return
	}
	_ = os.Remove(filepath.Join(env.dir, zone))
}

func (env *probeEnv) cleanup() {
	for _, zone := range env.made {
		env.remove(zone)
	}
	_ = env.flush()
}

func (env *probeEnv) listed(zone string) (bool, error) {
	text, err := env.scutil()
	if err != nil {
		return false, err
	}
	return len(parseScutilDNS(text)[zone]) > 0, nil
}

func (env *probeEnv) waitGone(zone string) {
	deadline := env.now().Add(env.giveUp)
	for env.now().Before(deadline) && !env.stopped() {
		if ok, err := env.listed(zone); err == nil && !ok {
			return
		}
		env.sleep(env.poll)
	}
}

// measure writes one zone and waits for macOS to list it.
func (env *probeEnv) measure(m probeMethod, zone string, iteration int) (probeResult, error) {
	res := probeResult{method: m.name, iteration: iteration}
	if err := env.guard(zone); err != nil {
		return res, err
	}
	env.made = append(env.made, zone)
	start := env.now()
	if err := m.write(env, zone); err != nil {
		return res, err
	}
	for !env.stopped() {
		ok, err := env.listed(zone)
		if err != nil {
			return res, err
		}
		elapsed := env.now().Sub(start)
		if ok {
			res.listed, res.latency = true, elapsed
			return res, nil
		}
		if elapsed >= env.giveUp {
			res.latency = elapsed
			return res, nil
		}
		if !res.republished && elapsed >= env.nudgeAt {
			res.republished = true
			if err := env.platform().SetResolver(zone, netip.MustParseAddr(probeNameserver)); err != nil {
				return res, err
			}
			_ = env.flush()
		}
		env.sleep(env.poll)
	}
	return res, nil
}

func runProbe(env *probeEnv) ([]probeResult, error) {
	var results []probeResult
	methods := probeMethods()
	for i := 0; i < env.n && !env.stopped(); i++ {
		for mi, m := range methods {
			zone := scratchZone(env.runID, mi, i)
			res, err := env.measure(m, zone, i+1)
			if err != nil {
				return results, err
			}
			results = append(results, res)
			fmt.Fprintf(env.out, "  run %2d  %-22s %s\n", i+1, m.name, describeProbe(res))
			env.remove(zone)
			env.waitGone(zone)
		}
	}
	return results, nil
}

func describeProbe(r probeResult) string {
	switch {
	case r.listed && r.republished:
		return fmt.Sprintf("listed after %s (rescued by a re-publish)", r.latency.Round(time.Millisecond))
	case r.listed:
		return fmt.Sprintf("listed after %s", r.latency.Round(time.Millisecond))
	case r.republished:
		return fmt.Sprintf("NOT listed after %s, even after a re-publish", r.latency.Round(time.Millisecond))
	default:
		return fmt.Sprintf("NOT listed after %s", r.latency.Round(time.Millisecond))
	}
}

// probeTable renders one row per method.
func probeTable(results []probeResult) string {
	order := []string{}
	byMethod := map[string][]probeResult{}
	for _, r := range results {
		if _, ok := byMethod[r.method]; !ok {
			order = append(order, r.method)
		}
		byMethod[r.method] = append(byMethod[r.method], r)
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "%-24s %5s %7s %9s %12s %9s %9s %9s\n", "method", "runs", "listed", "unlisted", "re-published", "min", "median", "max")
	for _, name := range order {
		rs := byMethod[name]
		var lat []time.Duration
		listed, republished := 0, 0
		for _, r := range rs {
			if r.listed {
				listed++
				lat = append(lat, r.latency)
			}
			if r.republished {
				republished++
			}
		}
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		min, med, max := "-", "-", "-"
		if len(lat) > 0 {
			min = lat[0].Round(time.Millisecond).String()
			med = lat[len(lat)/2].Round(time.Millisecond).String()
			max = lat[len(lat)-1].Round(time.Millisecond).String()
		}
		fmt.Fprintf(&b, "%-24s %5d %7d %9d %12d %9s %9s %9s\n", name, len(rs), listed, len(rs)-listed, republished, min, med, max)
	}
	return b.String()
}

// The real run: root, the real /etc/resolver, the real scutil.
func TestDNSPublishProbe(t *testing.T) {
	if os.Getenv("RUNOS_DNS_PROBE") != "1" {
		t.Skip("opt-in: run it with `make dnsprobe`")
	}
	if os.Geteuid() != 0 {
		t.Fatal("the probe writes /etc/resolver and needs root: run it with `make dnsprobe`")
	}
	n := 10
	if v, err := strconv.Atoi(os.Getenv("RUNOS_DNS_PROBE_N")); err == nil && v > 0 {
		n = v
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	env := &probeEnv{
		dir: resolverDir,
		scutil: func() (string, error) {
			out, err := runBounded("scutil", "--dns")
			return string(out), err
		},
		flush:   darwinPlatform{}.FlushDNS,
		now:     time.Now,
		sleep:   time.Sleep,
		out:     os.Stdout,
		n:       n,
		runID:   strconv.Itoa(os.Getpid()),
		poll:    50 * time.Millisecond,
		giveUp:  30 * time.Second,
		nudgeAt: 8 * time.Second,
		stopped: func() bool { return ctx.Err() != nil },
	}
	defer env.cleanup()

	fmt.Printf("DNS publish probe: %d run(s) per method, scratch zones under %s\n", n, probeZoneSuffix)
	results, err := runProbe(env)
	if err != nil {
		t.Fatalf("probe stopped: %v", err)
	}
	fmt.Println()
	fmt.Print(probeTable(results))
	if ctx.Err() != nil {
		fmt.Println("\ninterrupted: the table covers the runs that finished; the scratch files are removed")
	}
}
