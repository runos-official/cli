//go:build probe && darwin

package vpn

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

/*
The four ways the probe publishes a scratch zone, and the rules that keep it from touching anything
that is not its own.

  A  the OLD resolver-file write: open with truncate, then write (what the daemon did before).
  B  the production atomic write: temp file, then rename.
  C  B, then a network-change nudge as root (notifyutil -p com.apple.system.config.network_change).
  D  the dynamic-store entry the daemon now publishes first (dns_store.go), IPv4 entry and DNS entry.
  E  (opt-in, RUNOS_DNS_PROBE_EXTRA=1) the DNS entry of D alone, without the IPv4 entry, to tell
     whether the IPv4 entry matters.

Every method ends with the DNS flush the daemon does after a write, so the table compares what the
daemon would do, not a bare write.

Two namespaces keep a run apart from a live daemon. Scratch resolver files are dnsprobe-*.example.test
under the RunOS marker. Scratch store entries live under com.runos.vpnprobe.<run>-<method>-<n>, which
the daemon's own sweep (com.runos.vpn[.<resolver>]) does not match, so neither deletes the other's.
The files still carry the daemon's marker, so the probe refuses to start while the VPN is up
(preflight).
*/

const (
	probeStorePrefix = "com.runos.vpnprobe."
	probeInterface   = "lo0"
	probeClient      = "192.0.2.2"
)

// probeStoreKey is the only shape of store key the probe sets or removes.
var probeStoreKey = regexp.MustCompile(`^State:/Network/Service/com\.runos\.vpnprobe\.[0-9]+-[0-9]+-[0-9]+/(DNS|IPv4)$`)

type probeMethod struct {
	name string
	// write publishes the zone and flushes DNS.
	write func(env *probeEnv, zone string) error
	// republish is the once-only nudge for a zone that stays unlisted.
	republish func(env *probeEnv, zone string) error
	remove    func(env *probeEnv, zone string)
}

func probeMethods(extra bool) []probeMethod {
	atomic := func(env *probeEnv, zone string) error {
		if err := env.platform().SetResolver(zone, netip.MustParseAddr(probeNameserver)); err != nil {
			return err
		}
		return env.flush()
	}
	methods := []probeMethod{
		{"A truncate+write file", func(env *probeEnv, zone string) error {
			content := resolverMarker + "\nnameserver " + probeNameserver + "\n"
			if err := os.WriteFile(filepath.Join(env.dir, zone), []byte(content), 0o644); err != nil {
				return err
			}
			return env.flush()
		}, atomic, (*probeEnv).removeFile},
		{"B atomic file", atomic, atomic, (*probeEnv).removeFile},
		{"C atomic file + nudge", func(env *probeEnv, zone string) error {
			if err := atomic(env, zone); err != nil {
				return err
			}
			if err := env.nudge(); err != nil {
				return fmt.Errorf("network-change nudge: %w", err)
			}
			return env.flush()
		}, atomic, (*probeEnv).removeFile},
		{"D dynamic store", func(env *probeEnv, zone string) error { return env.storeWrite(zone, true) },
			func(env *probeEnv, zone string) error { return env.storeWrite(zone, true) }, (*probeEnv).removeStore},
	}
	if extra {
		methods = append(methods, probeMethod{"E store, DNS entry only",
			func(env *probeEnv, zone string) error { return env.storeWrite(zone, false) },
			func(env *probeEnv, zone string) error { return env.storeWrite(zone, false) }, (*probeEnv).removeStore})
	}
	return methods
}

// scratchZone is the only kind of zone the probe touches.
func scratchZone(runID string, method, i int) string {
	return fmt.Sprintf("%s%s-%d-%d%s", probeZonePrefix, runID, method, i, probeZoneSuffix)
}

var probeScratchTag = regexp.MustCompile(`^[0-9]+-[0-9]+-[0-9]+$`)

func isScratchZone(zone string) bool {
	return strings.HasPrefix(zone, probeZonePrefix) && strings.HasSuffix(zone, probeZoneSuffix) &&
		probeScratchTag.MatchString(strings.TrimSuffix(strings.TrimPrefix(zone, probeZonePrefix), probeZoneSuffix))
}

func probeStoreID(zone string) string {
	return probeStorePrefix + strings.TrimSuffix(strings.TrimPrefix(zone, probeZonePrefix), probeZoneSuffix)
}

func probeStoreKeys(zone string) []string {
	return []string{
		dnsStoreKeyRoot + probeStoreID(zone) + "/DNS",
		dnsStoreKeyRoot + probeStoreID(zone) + "/IPv4",
	}
}

// guard refuses to write over, or remove, a resolver file that is not a scratch zone or not ours.
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

// guardStore refuses any store key that is not one of the probe's own scratch keys.
func guardStore(keys ...string) error {
	for _, key := range keys {
		if !probeStoreKey.MatchString(key) {
			return fmt.Errorf("refusing to touch store key %q: not a probe scratch key", key)
		}
	}
	return nil
}

func (env *probeEnv) platform() darwinPlatform { return darwinPlatform{dir: env.dir} }

func (env *probeEnv) removeFile(zone string) {
	if env.guard(zone) != nil {
		return
	}
	_ = os.Remove(filepath.Join(env.dir, zone))
}

func (env *probeEnv) storeWrite(zone string, withIPv4 bool) error {
	if !isScratchZone(zone) {
		return fmt.Errorf("refusing to touch %q: not a scratch zone", zone)
	}
	if err := guardStore(probeStoreKeys(zone)...); err != nil {
		return err
	}
	svc := storeService{ID: probeStoreID(zone), Resolver: netip.MustParseAddr(probeNameserver), Zones: []string{zone}}
	iface, client := "", netip.Addr{}
	if withIPv4 {
		iface, client = probeInterface, netip.MustParseAddr(probeClient)
	}
	if out, err := env.store(storeApplyScript(svc, iface, client)); err != nil {
		return fmt.Errorf("write the scratch store entry: %w: %s", err, strings.TrimSpace(out))
	}
	return env.flush()
}

// removeStore removes the scratch keys of one zone, and only keys the guard accepts.
func (env *probeEnv) removeStore(zone string) {
	keys := probeStoreKeys(zone)
	if !isScratchZone(zone) || guardStore(keys...) != nil {
		return
	}
	var b strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&b, "remove %s\n", key)
	}
	_, _ = env.store(b.String())
}

// preflight refuses to start while the daemon's own DNS state is on the machine: its sweep would
// delete the scratch resolver files, and the probe would be measuring a machine it is fighting.
func (env *probeEnv) preflight() error {
	out, err := env.store(storeListScript())
	if err != nil {
		return fmt.Errorf("cannot read the dynamic store: %w", err)
	}
	if len(parseStoreKeys(out)) > 0 {
		return errors.New("the VPN's private DNS is active on this Mac: turn the VPN off first (runos vpn down), then run the probe again")
	}
	entries, _ := os.ReadDir(env.dir)
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(env.dir, e.Name()))
		if err == nil && isManagedResolverFile(data) && !isScratchZone(e.Name()) {
			return errors.New("a RunOS resolver file is on this Mac: turn the VPN off first (runos vpn down), then run the probe again")
		}
	}
	return nil
}

// sweepLeftovers removes scratch files and entries a killed earlier run left behind.
func (env *probeEnv) sweepLeftovers() {
	entries, _ := os.ReadDir(env.dir)
	for _, e := range entries {
		if isScratchZone(e.Name()) {
			env.removeFile(e.Name())
		}
	}
	out, err := env.store("list State:/Network/Service/com[.]runos[.]vpnprobe\n")
	if err != nil {
		return
	}
	var b strings.Builder
	for _, line := range strings.Split(out, "\n") {
		if _, key, ok := strings.Cut(line, "] = "); ok && guardStore(strings.TrimSpace(key)) == nil {
			fmt.Fprintf(&b, "remove %s\n", strings.TrimSpace(key))
		}
	}
	if b.Len() > 0 {
		_, _ = env.store(b.String())
	}
}

// probeAllowed says whether this process may run the real probe: only the make target, as root, via sudo.
func probeAllowed(getenv func(string) string, euid int) error {
	switch {
	case getenv("RUNOS_DNS_PROBE") != "1":
		return errors.New("opt-in: run it with `make dnsprobe`")
	case getenv("RUNOS_DNS_PROBE_VIA") != "make":
		return errors.New("run it with `make dnsprobe`, which builds it as you and runs it under sudo")
	case euid != 0 || getenv("SUDO_USER") == "":
		return errors.New("the probe writes the system DNS configuration and must be started with sudo by `make dnsprobe`")
	}
	return nil
}
