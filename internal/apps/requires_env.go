package apps

import (
	"sort"
)

// ServerInjectedEnvCollision is a single env var name that appears both
// as a key in the local .env file AND as a value in some
// requires.<alias>.env mapping. Because the right-hand side of the
// requires.env map names env vars the platform injects at runtime
// with the linked service's connection string, any local value for
// that name is dead config (or worse, divergent: a hand-authored
// connection string that disagrees with requires.<alias>.config).
type ServerInjectedEnvCollision struct {
	EnvVar string // env var name in the local .env (e.g. "DATABASE_URL")
	Alias  string // requires alias claiming the name (e.g. "poll-app-db")
	Field  string // credential-field key on the linked service (e.g. "url")
}

// FindServerInjectedEnvCollisions returns every entry in localEnv whose
// key matches an env var name claimed by some requires.<alias>.env value.
// Order is deterministic (sorted by env var name, then alias) so the
// output is stable for tests.
//
// Use case: filtering. A platform-claimed key (DATABASE_URL, REDIS_HOST,
// etc.) legitimately appears in the local secret env file — apps_pull
// writes it there so local matches the K8s Secret, and the pre-deploy
// merge re-introduces it on every deploy. The previous CLI surfaced a
// "remove these, they're dead config" note on every deploy/sync, which
// was wrong-headed (removing them makes local less accurate, not more,
// since the next pull writes them back). Now the only consumer is
// runos deploy's warnLocalDeletions filter: the "got merged back"
// warning skips platform-claimed keys because for those, re-merging is
// the design, not a deletion that didn't take effect.
func FindServerInjectedEnvCollisions(localEnv map[string]string, requires map[string]ServiceRequirement) []ServerInjectedEnvCollision {
	if len(localEnv) == 0 || len(requires) == 0 {
		return nil
	}
	var out []ServerInjectedEnvCollision
	for alias, req := range requires {
		for field, envName := range req.Env {
			if envName == "" {
				continue
			}
			if _, present := localEnv[envName]; present {
				out = append(out, ServerInjectedEnvCollision{
					EnvVar: envName,
					Alias:  alias,
					Field:  field,
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].EnvVar != out[j].EnvVar {
			return out[i].EnvVar < out[j].EnvVar
		}
		return out[i].Alias < out[j].Alias
	})
	return out
}

