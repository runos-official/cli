package mcp

import "github.com/runos-official/cli/internal/manifest"

func (s *Server) mapType(t string) string {
	switch t {
	case "integer":
		return "number"
	case "array":
		return "array"
	case "boolean":
		return "boolean"
	case "object":
		return "object"
	default:
		return "string"
	}
}

// projectObjectValue builds the JSON Schema `additionalProperties`
// descriptor for an object field. Priority order:
//
//  1. providerOptions (domains_create / domains_update): return nil so
//     the constraint is dropped entirely and booleans round-trip
//     natively. (Existing carve-out, preserved.)
//  2. Manifest declares `valueType` (and optionally `valueFields` for
//     object values): emit the rich schema so map values are typed.
//  3. requires (apps/add / apps/update / deploy): hardcoded fallback
//     for the map[alias] → {id, type, config, env} shape until the
//     conductor manifest grows valueType/valueFields for it. Closes
//     I26-N. Removable once the manifest entry ships.
//  4. Default: legacy `{type: "string"}` for back-compat with every
//     map[string]string field whose manifest entry pre-dates
//     valueType.
func (s *Server) projectObjectValue(field manifest.Field) *Property {
	if field.Name == "providerOptions" {
		return nil
	}
	if field.ValueType != "" {
		mapped := s.mapType(field.ValueType)
		value := &Property{Type: mapped}
		if mapped == "object" && len(field.ValueFields) > 0 {
			value.Properties = make(map[string]Property, len(field.ValueFields))
			for _, sub := range field.ValueFields {
				subProp := Property{
					Type:        s.mapType(sub.Type),
					Description: sub.Description,
				}
				if len(sub.Enum) > 0 {
					subProp.Enum = sub.Enum
				}
				if sub.Default != nil {
					subProp.Default = sub.Default
				}
				value.Properties[sub.Name] = subProp
				if sub.Required {
					value.Required = append(value.Required, sub.Name)
				}
			}
		}
		return value
	}
	if field.Name == "requires" {
		// Hardcoded transition-window fallback for I26-N: map[alias] →
		// {id, type, config?, env?}. Conductor's manifest doesn't carry
		// valueType/valueFields for `requires` yet; this entry can be
		// dropped once it does (the `ValueType != ""` branch above
		// will take over automatically).
		return &Property{
			Type: "object",
			Properties: map[string]Property{
				"id":   {Type: "string", Description: "The 5-char osid of an existing service to link to. Required."},
				"type": {Type: "string", Description: "Service type slug (postgresql, valkey, mysql, ...). Required; must match the linked service's type."},
				"config": {
					Type:                 "object",
					AdditionalProperties: &Property{Type: "string"},
					Description:          "Optional config-map keyed env vars injected into the app pod (read from the service's stored config; e.g. DATABASE → DATABASE_NAME).",
				},
				"env": {
					Type:                 "object",
					AdditionalProperties: &Property{Type: "string"},
					Description:          "Optional secret-map keyed env vars injected into the app pod (read from the service's generated secrets; e.g. URL → DATABASE_URL).",
				},
			},
			Required: []string{"id", "type"},
		}
	}
	return &Property{Type: "string"}
}

// projectArrayItems builds the JSON Schema `items` descriptor for an
// array field. When the manifest declares `itemType` (and optionally
// `itemFields` for object elements), the projection emits the richer
// shape so LLMs that strict-validate the tool schema send the correct
// element type. Defaults to `{type: "string"}` for back-compat with
// existing []string fields whose manifest entries pre-date itemType.
//
// Regression target: I6-H. The conductor's R2 manifest bump added
// itemType + itemFields to apps/secret-files/update.add and
// apps/secrets/update.add but the projection bridge here kept emitting
// items.type=string, so MCP clients that followed the schema strictly
// still hit "Each file in 'add' must have a 'filename' string" on the
// server.
func (s *Server) projectArrayItems(field manifest.Field) *Property {
	itemType := field.ItemType
	if itemType == "" {
		return &Property{Type: "string"}
	}
	mapped := s.mapType(itemType)
	items := &Property{Type: mapped}
	if mapped == "object" && len(field.ItemFields) > 0 {
		items.Properties = make(map[string]Property, len(field.ItemFields))
		for _, sub := range field.ItemFields {
			subProp := Property{
				Type:        s.mapType(sub.Type),
				Description: sub.Description,
			}
			if len(sub.Enum) > 0 {
				subProp.Enum = sub.Enum
			}
			if sub.Default != nil {
				subProp.Default = sub.Default
			}
			items.Properties[sub.Name] = subProp
			if sub.Required {
				items.Required = append(items.Required, sub.Name)
			}
		}
	}
	return items
}
