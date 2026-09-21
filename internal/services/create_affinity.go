package services

import "github.com/runos-official/cli/internal/manifest"

// normalizeCreateAffinity omits a present null affinity value only when the
// add command declares an optional array in the request body. It leaves the
// caller's map intact, including any nested values.
func normalizeCreateAffinity(fields map[string]any, addCmd *manifest.Command) map[string]any {
	value, present := fields["nodeAffinityTags"]
	if !present || value != nil || addCmd == nil || addCmd.Input == nil {
		return fields
	}
	for _, field := range addCmd.Input.Fields {
		if field.Name != "nodeAffinityTags" || field.Type != "array" || field.Required || field.Positional {
			continue
		}
		copyFields := make(map[string]any, len(fields)-1)
		for name, candidate := range fields {
			if name != "nodeAffinityTags" {
				copyFields[name] = candidate
			}
		}
		return copyFields
	}
	return fields
}
