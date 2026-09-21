package services

import "encoding/json"

// MarshalJSON keeps an empty create body visible in a create plan.
// A nil create body remains absent in an update plan.
func (p *SyncPlan) MarshalJSON() ([]byte, error) {
	type planAlias SyncPlan
	var createBody *map[string]any
	if p.CreateBody != nil {
		createBody = &p.CreateBody
	}
	return json.Marshal(&struct {
		CreateBody *map[string]any `json:"createBody,omitempty"`
		*planAlias
	}{
		CreateBody: createBody,
		planAlias:  (*planAlias)(p),
	})
}
