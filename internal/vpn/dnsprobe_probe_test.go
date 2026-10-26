//go:build probe && darwin

package vpn

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"syscall"
	"testing"
	"time"
)

/*
The opt-in probe for FCR 1730: which way of publishing private DNS does macOS pick up, and how fast?

It is NOT part of `go test`. It needs root, because it writes the real /etc/resolver and the system
dynamic store, so it only runs through `make dnsprobe`, which builds it as you and runs the binary
under sudo. With the VPN OFF (it refuses to start otherwise) it:

  - uses scratch zones under example.test and the documentation address 192.0.2.53, never a real zone;
  - publishes each zone with four methods, interleaved so a slow minute does not favour one:
    A the old truncate+write file, B the atomic file, C the atomic file plus a network-change nudge as
    root, D the dynamic-store entry (E, the store's DNS entry alone, with RUNOS_DNS_PROBE_EXTRA=1);
  - polls `scutil --dns` until the zone is listed and records how long that took;
  - if a zone is still unlisted after a while, re-publishes it once and records whether that rescued it;
  - removes every scratch file and store entry it made, on exit and on Ctrl-C, and only ones that carry
    the RunOS marker (files) or the probe's own service id (store). It refuses to touch anything else.

Settings (environment): RUNOS_DNS_PROBE=1 and RUNOS_DNS_PROBE_VIA=make (both set by the make target),
RUNOS_DNS_PROBE_N runs per method (default 5), RUNOS_DNS_PROBE_EXTRA=1 to add method E.

Nothing in this file was run against macOS by the author: there was no root. The logic tests
(dnsprobe_logic_probe_test.go) run it on a fake Mac.
*/

const (
	probeZonePrefix = "dnsprobe-"
	probeZoneSuffix = ".example.test"
	probeNameserver = "192.0.2.53"
)

type probeEnv struct {
	dir     string
	scutil  func() (string, error)
	store   func(script string) (string, error)
	flush   func() error
	nudge   func() error
	now     func() time.Time
	sleep   func(time.Duration)
	out     io.Writer
	n       int
	runID   string
	poll    time.Duration
	giveUp  time.Duration
	nudgeAt time.Duration
	extra   bool
	// stopped reports that the operator interrupted the run.
	stopped func() bool
	made    []probeMade
}

// probeMade is one scratch zone and the method that published it, so cleanup removes it the right way.
type probeMade struct {
	method int
	zone   string
}

type probeResult struct {
	method      string
	iteration   int
	latency     time.Duration
	listed      bool
	republished bool
}

func (env *probeEnv) cleanup() {
	methods := probeMethods(env.extra)
	for _, m := range env.made {
		methods[m.method].remove(env, m.zone)
	}
	env.made = nil
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

// measure publishes one zone with a method and waits for macOS to list it.
func (env *probeEnv) measure(mi int, m probeMethod, zone string, iteration int) (probeResult, error) {
	res := probeResult{method: m.name, iteration: iteration}
	if err := env.guard(zone); err != nil {
		return res, err
	}
	env.made = append(env.made, probeMade{mi, zone})
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
			if err := m.republish(env, zone); err != nil {
				return res, err
			}
		}
		env.sleep(env.poll)
	}
	return res, nil
}

func runProbe(env *probeEnv) ([]probeResult, error) {
	var results []probeResult
	methods := probeMethods(env.extra)
	for i := 0; i < env.n && !env.stopped(); i++ {
		for mi, m := range methods {
			if env.stopped() {
				break
			}
			zone := scratchZone(env.runID, mi, i)
			res, err := env.measure(mi, m, zone, i+1)
			if err != nil {
				return results, err
			}
			results = append(results, res)
			fmt.Fprintf(env.out, "  run %2d  %-24s %s\n", i+1, m.name, describeProbe(res))
			m.remove(env, zone)
			_ = env.flush()
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

// probeTable renders one row per method, in the order the methods ran.
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
	fmt.Fprintf(&b, "%-26s %5s %7s %9s %12s %9s %9s %9s\n", "method", "runs", "listed", "unlisted", "re-published", "min", "median", "max")
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
		fmt.Fprintf(&b, "%-26s %5d %7d %9d %12d %9s %9s %9s\n", name, len(rs), listed, len(rs)-listed, republished, min, med, max)
	}
	return b.String()
}

// The real run: root via sudo from the make target, the real /etc/resolver, the real store and scutil.
func TestDNSPublishProbe(t *testing.T) {
	if os.Getenv("RUNOS_DNS_PROBE") != "1" {
		t.Skip("opt-in: run it with `make dnsprobe`")
	}
	if err := probeAllowed(os.Getenv, os.Geteuid()); err != nil {
		t.Fatal(err)
	}
	n := 5
	if v, err := strconv.Atoi(os.Getenv("RUNOS_DNS_PROBE_N")); err == nil && v > 0 {
		n = v
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	env := &probeEnv{
		dir: resolverDir,
		scutil: func() (string, error) {
			out, err := runBounded("scutil", "--dns")
			return string(out), err
		},
		store: func(script string) (string, error) {
			out, err := runWithInput(script, "scutil")
			return string(out), err
		},
		flush: darwinPlatform{}.FlushDNS,
		nudge: func() error {
			out, err := run("notifyutil", "-p", "com.apple.system.config.network_change")
			if err != nil {
				return fmt.Errorf("%w: %s", err, out)
			}
			return nil
		},
		now:     time.Now,
		sleep:   time.Sleep,
		out:     os.Stdout,
		n:       n,
		runID:   strconv.Itoa(os.Getpid()),
		poll:    50 * time.Millisecond,
		giveUp:  30 * time.Second,
		nudgeAt: 8 * time.Second,
		extra:   os.Getenv("RUNOS_DNS_PROBE_EXTRA") == "1",
		stopped: func() bool { return ctx.Err() != nil },
	}
	if err := env.preflight(); err != nil {
		t.Fatal(err)
	}
	env.sweepLeftovers()
	defer env.cleanup()

	fmt.Printf("DNS publish probe: %d run(s) per method, scratch zones under %s\n", n, probeZoneSuffix)
	results, err := runProbe(env)
	if err != nil {
		t.Fatalf("probe stopped: %v", err)
	}
	fmt.Println()
	fmt.Print(probeTable(results))
	if ctx.Err() != nil {
		fmt.Println("\ninterrupted: the table covers the runs that finished; the scratch files and entries are removed")
	}
}
