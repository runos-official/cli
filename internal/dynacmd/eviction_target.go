package dynacmd

import (
	"time"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/manifest"
	"github.com/spf13/cobra"
)

const evictionLookupFailed = " (node identity lookup failed)"

func isEvictionHostname(cmdDef manifest.Command, fieldName string) bool {
	return cmdDef.Command == "storage-groups/evict-node" && flagNameFor(fieldName) == "hostname"
}

// evictionHostnameSuffix uses the server's resolution without repeating its matching rules.
func evictionHostnameSuffix(c *cobra.Command, cmdDef manifest.Command, args []string, hostname string) string {
	result := make(chan *api.EvictionTarget, 1)
	done := nodeNameWorkerDone
	loader := loadNodeNameConfig
	timer := time.NewTimer(nodeNameDeadline)
	defer timer.Stop()
	go func() {
		defer func() {
			if done != nil {
				done()
			}
		}()
		cfg, err := loader()
		if err != nil || cfg == nil || !auth.HasCredentials(cfg) {
			result <- nil
			return
		}
		accountID := cfg.GetAccountID()
		clusterID := nodeNameClusterID(c, cmdDef, args, cfg)
		if accountID == "" || clusterID == "" {
			result <- nil
			return
		}
		token, err := auth.ResolveToken(cfg)
		if err != nil || token == "" {
			result <- nil
			return
		}
		client := api.NewClientWithTimeout(cfg.GetAPIURLQuiet(), nodeNameDeadline)
		target, err := client.ReadEvictionTarget(accountID, clusterID, "", hostname, token)
		if err != nil {
			result <- nil
			return
		}
		result <- target
	}()
	select {
	case target := <-result:
		return evictionTargetSuffix(target)
	case <-timer.C:
		return evictionLookupFailed
	}
}

func evictionTargetSuffix(target *api.EvictionTarget) string {
	if target == nil || target.Outcome == api.EvictionMissingHostname {
		return evictionLookupFailed
	}
	var suffix string
	switch target.Outcome {
	case api.EvictionMatched:
		suffix = " nid=" + target.RunosNID
		if name := nodeLabel(target.RunosNodeName); name != "" {
			suffix += " name=" + name
		}
	case api.EvictionNoRecord:
		if target.NID == nil {
			suffix = " (RunOS has no record of this machine)"
		} else {
			suffix = " (no current RunOS node record; device records remain)"
		}
	case api.EvictionAmbiguous:
		suffix = " (ambiguous hostname; CLI refuses to name one node)"
	case api.EvictionUnknown:
		suffix = " (current node identity could not be established)"
	default:
		return evictionLookupFailed
	}
	if target.NID != nil {
		suffix += " (device-record cleanup nid=" + *target.NID + "; only matching records for this hostname would be removed)"
	}
	return suffix
}
