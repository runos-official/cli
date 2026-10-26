//go:build darwin

package vpn

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// runStore feeds a script to scutil and returns what it printed. It is the one place the store is
// reached, so a test replaces it (darwinPlatform.store) and never starts scutil.
func (p darwinPlatform) runStore(script string) (string, error) {
	if p.store != nil {
		return p.store(script)
	}
	out, err := runWithInput(script, "scutil")
	return string(out), err
}

// ownStoreKeys lists the keys in the dynamic store that are exactly ours. Anything else under the
// same prefix is another program's and never appears in the result.
func (p darwinPlatform) ownStoreKeys() ([]string, error) {
	out, err := p.runStore(storeListScript())
	if err != nil {
		return nil, fmt.Errorf("list the dynamic store: %w: %s", err, strings.TrimSpace(out))
	}
	return parseStoreKeys(out), nil
}

// ensureStore brings the store entries to the plan and reports whether it changed anything.
//
// It lists our keys, reads back each service that should exist, and writes only the ones that are
// missing or differ. A service the plan no longer has is removed with the rest of its keys. With
// republish set it removes and sets every service again: a key that disappears and returns is a fresh
// change for configd even when its content is the same. After a write it reads the entry back, because
// scutil prints "Permission denied" and still exits 0.
func (p darwinPlatform) ensureStore(iface string, client netip.Addr, resolvers []ResolverPlan, republish bool) (bool, error) {
	services, err := dnsStoreServices(resolvers)
	if err != nil {
		return false, err
	}
	keys, err := p.ownStoreKeys()
	if err != nil {
		return false, err
	}
	have := map[string]bool{}
	for _, key := range keys {
		have[key] = true
	}
	wantID := map[string]bool{}
	var script strings.Builder
	var written []storeService
	var cleared []string
	for _, svc := range services {
		wantID[svc.ID] = true
		needs := republish || !have[svc.dnsKey()] || (storeWantsIPv4(iface, client) && !have[svc.ipv4Key()])
		if !needs {
			out, err := p.runStore(storeShowScript(svc))
			entry, ok := parseStoreDNS(out)
			needs = err != nil || !ok || !svc.matches(entry)
		}
		if !needs {
			continue
		}
		if republish {
			cleared = append(cleared, svc.dnsKey(), svc.ipv4Key())
		}
		written = append(written, svc)
	}
	var stale []string
	for _, key := range keys {
		if !wantID[storeServiceOf(key)] {
			stale = append(stale, key)
		}
	}
	script.WriteString(storeRemoveScript(cleared))
	for _, svc := range written {
		script.WriteString(storeApplyScript(svc, iface, client))
	}
	script.WriteString(storeRemoveScript(stale))
	if script.Len() == 0 {
		return false, nil
	}
	if out, err := p.runStore(script.String()); err != nil {
		return false, fmt.Errorf("write the dynamic store: %w: %s", err, strings.TrimSpace(out))
	}
	for _, svc := range written {
		out, err := p.runStore(storeShowScript(svc))
		entry, ok := parseStoreDNS(out)
		if err != nil || !ok || !svc.matches(entry) {
			return false, fmt.Errorf("the dynamic store did not keep the DNS entry %s", svc.dnsKey())
		}
	}
	if len(stale) > 0 {
		if err := p.confirmStoreKeysGone(stale); err != nil {
			return true, err
		}
	}
	return true, nil
}

// storeWantsIPv4 says whether the apply script includes the IPv4 entry.
func storeWantsIPv4(iface string, client netip.Addr) bool {
	return storeIfaceName.MatchString(iface) && client.IsValid()
}

// confirmStoreKeysGone lists our keys again and fails when any of the given ones is still there.
func (p darwinPlatform) confirmStoreKeysGone(removed []string) error {
	left, err := p.ownStoreKeys()
	if err != nil {
		return err
	}
	gone := map[string]bool{}
	for _, key := range removed {
		gone[key] = true
	}
	n := 0
	for _, key := range left {
		if gone[key] {
			n++
		}
	}
	if n > 0 {
		return fmt.Errorf("%d RunOS DNS entries are still in the dynamic store after the removal", n)
	}
	return nil
}

// sweepStore removes every key that is ours, for Teardown and the start-up sweep. It is idempotent:
// with none present it runs one list and nothing else. It returns how many keys it removed.
func (p darwinPlatform) sweepStore() (int, error) {
	keys, err := p.ownStoreKeys()
	if err != nil {
		return 0, err
	}
	if len(keys) == 0 {
		return 0, nil
	}
	var errs []error
	if out, err := p.runStore(storeRemoveScript(keys)); err != nil {
		errs = append(errs, fmt.Errorf("remove from the dynamic store: %w: %s", err, strings.TrimSpace(out)))
	}
	if err := p.confirmStoreKeysGone(keys); err != nil {
		errs = append(errs, err)
		return 0, errors.Join(errs...)
	}
	return len(keys), errors.Join(errs...)
}
