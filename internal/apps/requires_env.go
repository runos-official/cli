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

// FilterPlatformInjectedEnv returns serverEnv with every key removed
// whose name appears as a value in any requires.<alias>.env mapping.
// These names are claimed by the platform: the conductor re-injects
// them on every deploy from the linked service's credentials, so a
// local file that lacks them isn't real drift, it's just code that
// doesn't read those particular vars yet.
//
// Used by the pre-deploy drift gate (and `apps diff`) to suppress the
// I3-E false-positive: a freshly-provisioned app has DATABASE_URL,
// REDIS_*, etc. on the server immediately, but the user's local
// `.secret.env` is empty (apps_pull writes them only after the user
// runs it explicitly). Without this filter, every "second deploy"
// would trip the drift gate even when the user had no local change
// to push.
//
// Returns serverEnv unchanged when either input is empty or no
// requires entry maps an env name (i.e. all entries are infra-only).
// Never mutates serverEnv; the result is a fresh map when filtering
// is needed, the original reference otherwise.
func FilterPlatformInjectedEnv(serverEnv map[string]string, requires map[string]ServiceRequirement) map[string]string {
	if len(serverEnv) == 0 || len(requires) == 0 {
		return serverEnv
	}
	injected := make(map[string]bool)
	for _, req := range requires {
		for _, envName := range req.Env {
			if envName != "" {
				injected[envName] = true
			}
		}
	}
	if len(injected) == 0 {
		return serverEnv
	}
	out := make(map[string]string, len(serverEnv))
	for k, v := range serverEnv {
		if injected[k] {
			continue
		}
		out[k] = v
	}
	return out
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

