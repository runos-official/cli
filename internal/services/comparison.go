package services

import (
	"sort"

	"github.com/runos-official/cli/internal/manifest"
)

// ServiceComparison describes actionable drift for an existing service.
// Callers retain responsibility for parsing and checking service identity.
type ServiceComparison struct {
	PatchBody map[string]any
	Removals  []string
	Refused   []string
	Diff      string
	HasDrift  bool
}

// CompareServiceState interprets a local service file against a server
// projection and the manifest's update contract. It does not modify inputs.
func CompareServiceState(local, server *ServiceYAML, updateCmd, showCmd *manifest.Command) ServiceComparison {
	var serverFields map[string]any
	if server != nil {
		serverFields = server.Fields
	}
	localFields := cloneFields(local.Fields)
	comparisonServerFields := cloneFields(serverFields)
	for name, field := range ClearableFields(updateCmd) {
		if _, supported := emptyValueFor(field.Type); !supported {
			continue
		}
		if value, present := local.Fields[name]; present {
			if value == nil {
				if stored, held := serverFields[name]; held {
					localFields[name] = stored
				} else {
					delete(localFields, name)
				}
			}
			continue
		}
		if isEmptyStoredValue(serverFields[name]) {
			delete(comparisonServerFields, name)
		}
	}

	effectiveLocal := *local
	effectiveLocal.Fields = localFields
	var effectiveServer *ServiceYAML
	if server != nil {
		copyServer := *server
		copyServer.Fields = comparisonServerFields
		effectiveServer = &copyServer
	}
	result := ServiceComparison{HasDrift: !servicesEqual(&effectiveLocal, effectiveServer)}
	if !result.HasDrift {
		return result
	}
	allowed := UpdateInputFieldNames(updateCmd)
	result.PatchBody = computeDriftPatch(localFields, serverFields, allowed)
	result.Refused = refusedDrift(localFields, serverFields, allowed, false, showFieldNames(showCmd))
	removals, removalRefusals := computeClearRemovals(local.Fields, serverFields, updateCmd)
	if len(removals) > 0 {
		if result.PatchBody == nil {
			result.PatchBody = make(map[string]any, len(removals))
		}
		for name, value := range removals {
			result.PatchBody[name] = value
		}
		result.Removals = removalNames(removals)
	}
	if len(removalRefusals) > 0 {
		result.Refused = append(result.Refused, removalRefusals...)
		sort.Strings(result.Refused)
	}
	result.Diff = renderFieldDiff(&effectiveLocal, effectiveServer)
	return result
}

func cloneFields(fields map[string]any) map[string]any {
	if fields == nil {
		return nil
	}
	copy := make(map[string]any, len(fields))
	for name, value := range fields {
		copy[name] = value
	}
	return copy
}
