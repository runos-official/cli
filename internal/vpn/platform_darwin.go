//go:build darwin

package vpn

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
)

// The macOS platform. Routes go through `route` against the utun interface; split DNS is
// `/etc/resolver/<zone>` files, each one line `nameserver <resolver>`, which is how macOS steers a
// single domain to a resolver without touching the machine's default DNS. `dig` bypasses this
// (it talks to the resolver directly), so the proof uses the system path: `dscacheutil -q host`
// or `curl`.
//
// Every resolver file the daemon writes carries a marker comment so Teardown removes exactly the
// daemon's files and never a hand-written one.

// defaultTunName is the interface the engine requests on macOS; the kernel appends the number.
const defaultTunName = "utun"

type darwinPlatform struct {
	// dir is the resolver directory; empty means /etc/resolver. Set by tests only, so they never
	// write to the real one.
	dir string
	// flush replaces the real DNS flush; nil means the real one. Set by tests only.
	flush func() error
	// fs replaces the real file system for the resolver writes; nil means the real one. Set by
	// tests only.
	fs resolverFS
	// scutil replaces `scutil --dns`; nil means the real one. Set by tests only.
	scutil func() (string, error)
}

func (p darwinPlatform) files() resolverFS {
	if p.fs != nil {
		return p.fs
	}
	return osResolverFS{}
}

// tempDir is where resolver files are staged before the rename: the parent of the resolver
// directory (/etc), which is on the same file system but is not the directory macOS watches.
func (p darwinPlatform) tempDir() string { return filepath.Dir(p.resolverPath()) }

func (p darwinPlatform) resolverPath() string {
	if p.dir != "" {
		return p.dir
	}
	return resolverDir
}

func (p darwinPlatform) flushDNS() error {
	if p.flush != nil {
		return p.flush()
	}
	return p.FlushDNS()
}

func newPlatform() platform { return darwinPlatform{} }

func (darwinPlatform) SetInterfaceAddress(iface string, addr netip.Prefix) error {
	ip := addr.Addr().String()
	// A utun is point-to-point; macOS `ifconfig utunN <ip> <ip>` sets both ends to the device's
	// own address, which is what a /32 client wants.
	if out, err := run("ifconfig", iface, "inet", ip, ip, "up"); err != nil {
		return fmt.Errorf("set interface address: %w: %s", err, out)
	}
	return nil
}

func (darwinPlatform) Routes(iface string) ([]netip.Prefix, error) {
	// `netstat -rn -f inet` lists the routing table; the daemon's routes are those whose gateway
	// or interface column is the utun. Parsing netstat is brittle, so the daemon instead treats
	// routes as write-only-diffable: it asks for none back and relies on AddRoute being
	// idempotent (route add of an existing route is a no-op the daemon tolerates). Returning an
	// empty set means "add whatever the plan wants"; RemoveRoute on teardown covers the rest.
	return nil, nil
}

func (darwinPlatform) AddRoute(iface string, prefix netip.Prefix) error {
	// -ifscope pins the route to the interface, and `route add` replaces an existing route rather
	// than failing, so this is idempotent.
	if out, err := run("route", "-q", "-n", "add", "-net", prefix.String(), "-interface", iface); err != nil {
		if strings.Contains(string(out), "File exists") {
			return nil
		}
		return fmt.Errorf("add route %s: %w: %s", prefix, err, out)
	}
	return nil
}

func (darwinPlatform) RemoveRoute(iface string, prefix netip.Prefix) error {
	if out, err := run("route", "-q", "-n", "delete", "-net", prefix.String(), "-interface", iface); err != nil {
		if strings.Contains(string(out), "not in table") {
			return nil
		}
		return fmt.Errorf("remove route %s: %w: %s", prefix, err, out)
	}
	return nil
}

func (p darwinPlatform) Resolvers() (map[string]netip.Addr, error) {
	found := map[string]netip.Addr{}
	entries, err := os.ReadDir(p.resolverPath())
	if err != nil {
		if os.IsNotExist(err) {
			return found, nil
		}
		return nil, fmt.Errorf("read %s: %w", p.resolverPath(), err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(p.resolverPath(), entry.Name())
		data, err := os.ReadFile(path)
		if err != nil || !isManagedResolverFile(data) {
			continue // not ours
		}
		if addr, ok := parseResolverNameserver(data); ok {
			found[entry.Name()] = addr
		}
	}
	return found, nil
}

func (p darwinPlatform) SetResolver(zone string, resolver netip.Addr) error {
	if err := os.MkdirAll(p.resolverPath(), 0o755); err != nil {
		logEvent("dns: create %s: %s", p.resolverPath(), err)
		return errors.New(dnsWriteText)
	}
	dest := filepath.Join(p.resolverPath(), zone)
	// A file the daemon did not write is the person's own, and replacing it would silently change how
	// their Mac resolves that zone. An empty file is the leftover of a write that was cut short by an
	// older daemon, which truncated before it wrote, so it is ours to replace.
	if existing, err := os.ReadFile(dest); err == nil && len(bytes.TrimSpace(existing)) > 0 && !isManagedResolverFile(existing) {
		logEvent("dns: %s exists and is not managed by RunOS; leaving it alone", dest)
		return errors.New(dnsHandAuthoredText)
	}
	content := fmt.Sprintf("%s\nnameserver %s\n", resolverMarker, resolver.String())
	if err := writeFileAtomic(p.files(), p.tempDir(), dest, []byte(content), 0o644); err != nil {
		logEvent("dns: write %s: %s", dest, err)
		return errors.New(dnsWriteText)
	}
	return nil
}

func (p darwinPlatform) RemoveResolver(zone string) error {
	path := filepath.Join(p.resolverPath(), zone)
	// Only remove a file the daemon wrote, never a hand-authored /etc/resolver entry.
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !isManagedResolverFile(data) {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		logEvent("dns: remove %s: %s", path, err)
		return errors.New(dnsRemoveText)
	}
	return nil
}

func (darwinPlatform) FlushDNS() error {
	// Both are needed on modern macOS: dscacheutil clears the cache, mDNSResponder reloads it.
	_, _ = run("dscacheutil", "-flushcache")
	_, _ = run("killall", "-HUP", "mDNSResponder")
	return nil
}

func (p darwinPlatform) Teardown(iface string) error {
	// A killed daemon can leave a staged temp file next to the resolver directory.
	removeStaleResolverTemps(p.files(), p.tempDir())
	resolvers, err := p.Resolvers()
	if err != nil {
		return err
	}
	for zone := range resolvers {
		if rmErr := p.RemoveResolver(zone); rmErr != nil {
			err = rmErr
		}
	}
	// Only when something was removed. Teardown also runs as the sweep on every daemon start, and a
	// flush there reloaded the machine's resolver for nothing.
	if len(resolvers) > 0 {
		_ = p.flushDNS()
	}
	// The interface's routes go with the interface when the engine closes it, so there is nothing
	// to delete by prefix here; RemoveRoute is used for a single disconnected cluster while the
	// tunnel stays up.
	return err
}
