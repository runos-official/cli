package apps

import (
	"fmt"
	"sort"
	"strings"
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
// output is stable for tests and stable in CI logs.
//
// Use case: gate apps_sync (refuse) and runos deploy (warn) against AI-
// or human-authored env files that hand-wrote DATABASE_URL / REDIS_URL /
// etc., not realising the platform provides those at runtime from the
// linked service's credentials.
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

// FormatServerInjectedEnvCollisions renders a heads-up message naming
// each entry in the local env file whose key is also claimed by a
// requires.<alias>.env mapping. envFile is the path the message points
// the user at; pass an empty string to print just "your env file".
//
// Conductor drops these names from customEnvVars at deploy/sync time
// and the platform-injected value wins at runtime, so the collision
// is no longer a runtime correctness problem; the local .env line is
// just dead config. The message is informational and points the user
// at the cosmetic cleanup. Used by both apps_sync and runos deploy.
func FormatServerInjectedEnvCollisions(cs []ServerInjectedEnvCollision, envFile string) string {
	if len(cs) == 0 {
		return ""
	}
	target := "your env file"
	if envFile != "" {
		target = envFile
	}
	var sb strings.Builder
	noun := "env keys are"
	if len(cs) == 1 {
		noun = "env key is"
	}
	fmt.Fprintf(&sb, "%d local %s claimed by requires.<alias>.env (the platform injects these at runtime):\n", len(cs), noun)
	maxName := 0
	for _, c := range cs {
		if len(c.EnvVar) > maxName {
			maxName = len(c.EnvVar)
		}
	}
	for _, c := range cs {
		fmt.Fprintf(&sb, "  %-*s   (claimed by requires.%s.env.%s)\n", maxName, c.EnvVar, c.Alias, c.Field)
	}
	fmt.Fprintf(&sb, "\nConductor drops these from customEnvVars on deploy/sync, so the runtime env reflects the linked\n")
	fmt.Fprintf(&sb, "service's credentials regardless of what's in %s. The local line is harmless dead config; remove\n", target)
	fmt.Fprintf(&sb, "it to keep the file accurate.\n")
	return sb.String()
}
