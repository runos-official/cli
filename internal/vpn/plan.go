package vpn

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// The reconcile planner: the desired-state document in, the exact interface the machine should
// have out. Pure and network-free, so it is testable per requirement, and shared by every OS: the
// renderers (engine, routes, resolver files) each converge to a Plan and never read the document.

// PeerPlan is one WireGuard peer the engine must hold.
type PeerPlan struct {
	CID string
	// Hex, as the wireguard-go UAPI wants it (the document carries base64).
	PublicKeyHex string
	Endpoint     string
	AllowedIPs   []netip.Prefix
	// Seconds. Always set: a laptop is behind NAT far more often than not.
	Keepalive int
}

// ResolverPlan steers one DNS zone to one resolver over the tunnel.
type ResolverPlan struct {
	Zone     string
	Resolver netip.Addr
}

// Plan is everything the machine should have for this document.
type Plan struct {
	// The device's own address, /32.
	Address netip.Prefix
	// One per cluster that is connected, reachable, and covered by a live session; sorted by CID.
	Peers []PeerPlan
	// The union of every peer's AllowedIPs, sorted and deduplicated: what must be routed to the
	// interface.
	Routes []netip.Prefix
	// Sorted by zone.
	Resolvers []ResolverPlan
	// True when the tunnel must be torn down for the person to sign in again.
	LoginRequired bool
}

// KeepaliveSeconds is the persistent keepalive every peer gets. 25 s is the value WireGuard's
// own documentation gives for NAT traversal, and what the template configs carried.
const KeepaliveSeconds = 25

// Empty reports whether the plan asks for nothing: no address, no peers.
func (p Plan) Empty() bool {
	return !p.Address.IsValid() && len(p.Peers) == 0
}

// BuildPlan turns the document into a Plan. A cluster contributes a peer only when it is
// CONNECTED and REACHABLE and the session is live; an unreachable or disconnected cluster is
// left out entirely, and `loginRequired` empties the peer set (the servers have dropped this key
// or are about to, so keeping the peer would be a tunnel that sends and cannot receive).
//
// A malformed field (an unparsable address or prefix, a key that is not 32 bytes) fails the whole
// plan rather than a part of it: a document Conductor produced is never partly right, so a parse
// failure is a bug to surface, not a cluster to skip.
func BuildPlan(doc *Document) (Plan, error) {
	var plan Plan
	if doc == nil {
		return plan, fmt.Errorf("no document")
	}
	if doc.Device.Address != "" {
		address, err := netip.ParsePrefix(doc.Device.Address)
		if err != nil {
			return plan, fmt.Errorf("device address %q: %w", doc.Device.Address, err)
		}
		plan.Address = address
	}
	plan.LoginRequired = doc.Device.Session.LoginRequired
	if plan.LoginRequired {
		return plan, nil
	}

	routes := map[netip.Prefix]struct{}{}
	resolvers := map[string]netip.Addr{}
	for _, cluster := range doc.Clusters {
		if !cluster.Connected || !cluster.Server.Reachable {
			continue
		}
		keyHex, err := keyBase64ToHex(cluster.Server.PublicKey)
		if err != nil {
			return plan, fmt.Errorf("cluster %s server key: %w", cluster.CID, err)
		}
		if cluster.Server.Endpoint == "" {
			return plan, fmt.Errorf("cluster %s is reachable but has no endpoint", cluster.CID)
		}
		peer := PeerPlan{
			CID:          cluster.CID,
			PublicKeyHex: keyHex,
			Endpoint:     cluster.Server.Endpoint,
			Keepalive:    KeepaliveSeconds,
		}
		for _, raw := range cluster.AllowedIPs {
			prefix, err := netip.ParsePrefix(raw)
			if err != nil {
				return plan, fmt.Errorf("cluster %s allowed ip %q: %w", cluster.CID, raw, err)
			}
			prefix = prefix.Masked()
			peer.AllowedIPs = append(peer.AllowedIPs, prefix)
			routes[prefix] = struct{}{}
		}
		sortPrefixes(peer.AllowedIPs)
		plan.Peers = append(plan.Peers, peer)

		if cluster.DNS.Resolver != "" {
			resolver, err := netip.ParseAddr(cluster.DNS.Resolver)
			if err != nil {
				return plan, fmt.Errorf("cluster %s resolver %q: %w", cluster.CID, cluster.DNS.Resolver, err)
			}
			for _, zone := range cluster.DNS.Zones {
				zone = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zone)), ".")
				if zone == "" {
					continue
				}
				// Two connected clusters never share a zone (each has its own RunOS domain and its
				// own custom domains); if they ever did, the first by CID wins deterministically.
				if _, taken := resolvers[zone]; !taken {
					resolvers[zone] = resolver
				}
			}
		}
	}
	sort.Slice(plan.Peers, func(i, j int) bool { return plan.Peers[i].CID < plan.Peers[j].CID })
	for prefix := range routes {
		plan.Routes = append(plan.Routes, prefix)
	}
	sortPrefixes(plan.Routes)
	for zone, resolver := range resolvers {
		plan.Resolvers = append(plan.Resolvers, ResolverPlan{Zone: zone, Resolver: resolver})
	}
	sort.Slice(plan.Resolvers, func(i, j int) bool { return plan.Resolvers[i].Zone < plan.Resolvers[j].Zone })
	return plan, nil
}

func sortPrefixes(prefixes []netip.Prefix) {
	sort.Slice(prefixes, func(i, j int) bool { return prefixes[i].String() < prefixes[j].String() })
}

// keyBase64ToHex converts a WireGuard key from the base64 the API speaks to the hex the UAPI
// wants, refusing anything that is not exactly 32 bytes.
func keyBase64ToHex(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return "", fmt.Errorf("not base64: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("%d bytes, want 32", len(raw))
	}
	return hex.EncodeToString(raw), nil
}

// PrefixDiff is what a route or allowed-ip converge must add and remove to reach `want` from
// `have`. Order is deterministic (sorted by string form).
type PrefixDiff struct {
	Add    []netip.Prefix
	Remove []netip.Prefix
}

// DiffPrefixes computes the converge from `have` to `want`.
func DiffPrefixes(have, want []netip.Prefix) PrefixDiff {
	haveSet := map[netip.Prefix]struct{}{}
	for _, p := range have {
		haveSet[p.Masked()] = struct{}{}
	}
	wantSet := map[netip.Prefix]struct{}{}
	for _, p := range want {
		wantSet[p.Masked()] = struct{}{}
	}
	var diff PrefixDiff
	for p := range wantSet {
		if _, ok := haveSet[p]; !ok {
			diff.Add = append(diff.Add, p)
		}
	}
	for p := range haveSet {
		if _, ok := wantSet[p]; !ok {
			diff.Remove = append(diff.Remove, p)
		}
	}
	sortPrefixes(diff.Add)
	sortPrefixes(diff.Remove)
	return diff
}

// ResolverDiff is what the split-DNS renderer must write and delete: zones whose resolver is
// missing or different are in Set, zones present but no longer wanted are in Remove.
type ResolverDiff struct {
	Set    []ResolverPlan
	Remove []string
}

// DiffResolvers computes the converge from `have` (zone -> resolver as found on the machine) to
// the plan's resolvers.
func DiffResolvers(have map[string]netip.Addr, want []ResolverPlan) ResolverDiff {
	var diff ResolverDiff
	wanted := map[string]struct{}{}
	for _, r := range want {
		wanted[r.Zone] = struct{}{}
		if current, ok := have[r.Zone]; !ok || current != r.Resolver {
			diff.Set = append(diff.Set, r)
		}
	}
	for zone := range have {
		if _, ok := wanted[zone]; !ok {
			diff.Remove = append(diff.Remove, zone)
		}
	}
	sort.Slice(diff.Set, func(i, j int) bool { return diff.Set[i].Zone < diff.Set[j].Zone })
	sort.Strings(diff.Remove)
	return diff
}
