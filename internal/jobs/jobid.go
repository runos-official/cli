package jobs

import "regexp"

// jobIDPattern matches the canonical 8-4-4-4-12 hex shape conductor
// emits for every job id. Both upper- and lower-case hex are accepted
// since the conductor preserves whichever form the upstream generator
// used.
var jobIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidateJobID returns nil when id has the conductor's UUID shape and
// a descriptive error otherwise. Pure helper so the cobra layer can
// gate before hitting the network and surface a CLI-layer message
// instead of a 404 fall-through.
func ValidateJobID(id string) bool {
	return jobIDPattern.MatchString(id)
}
