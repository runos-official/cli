package vpn

import (
	"fmt"
	"net/netip"
)

// verifyEffectiveResolvers checks that the private DNS zones this session needs are the ones macOS
// will actually use.
//
// THE CALLER MUST GIVE IT TIME. Writing /etc/resolver/<zone> does not take effect the instant the
// file lands: macOS re-reads the directory and republishes its DNS configuration asynchronously, so
// a check that runs immediately after the write sees the zone missing and refuses.
//
// MEASURED 2026-08-20 on the operator's Mac: `runos vpn connect cl1` failed with "private DNS zone
// cl1.acct1.dev.runos.xyz is not effective", and the SAME command, unchanged, succeeded moments
// later. In between, `scutil --dns` already listed the zone with its nameserver and reach 0x2
// (Reachable), and /etc/resolver/cl1.acct1.dev.runos.xyz was on disk. Nothing was wrong with the
// cluster; the check was simply early.
//
// The refusal reads like a broken cluster, which is the worst part: it names a zone and no action.
// See VerifyEffectiveResolversWithin for the polling wrapper callers should use.
func verifyEffectiveResolvers(effective map[string][]netip.Addr, expected []ResolverPlan) error {
	for _, plan := range expected {
		servers := effective[plan.Zone]
		if len(servers) == 0 {
			return fmt.Errorf(
				"private DNS zone %s is not effective yet: macOS republishes its DNS configuration "+
					"a moment after the resolver file is written, so this is usually timing rather "+
					"than a broken cluster. Retrying the command normally works; check with "+
					"`scutil --dns | grep -A3 %s`",
				plan.Zone, plan.Zone)
		}
		for _, server := range servers {
			if server != plan.Resolver {
				return fmt.Errorf("private DNS zone %s is overridden by resolver %s", plan.Zone, server)
			}
		}
	}
	return nil
}
