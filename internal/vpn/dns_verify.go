package vpn

import (
	"fmt"
	"net/netip"
)

func verifyEffectiveResolvers(effective map[string][]netip.Addr, expected []ResolverPlan) error {
	for _, plan := range expected {
		servers := effective[plan.Zone]
		if len(servers) == 0 {
			return fmt.Errorf("private DNS zone %s is not effective", plan.Zone)
		}
		for _, server := range servers {
			if server != plan.Resolver {
				return fmt.Errorf("private DNS zone %s is overridden by resolver %s", plan.Zone, server)
			}
		}
	}
	return nil
}
