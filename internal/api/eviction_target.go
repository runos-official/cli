package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"
)

// EvictionOutcome is the server's advisory storage target resolution.
type EvictionOutcome string

const (
	EvictionMatched         EvictionOutcome = "matched"
	EvictionNoRecord        EvictionOutcome = "no_record"
	EvictionAmbiguous       EvictionOutcome = "ambiguous"
	EvictionUnknown         EvictionOutcome = "unknown"
	EvictionMissingHostname EvictionOutcome = "missing_hostname"
)

// EvictionSource identifies the source of independently established facts.
type EvictionSource string

const (
	EvictionDevices EvictionSource = "devices"
	EvictionNodes   EvictionSource = "nodes"
)

// EvictionTarget keeps device cleanup provenance separate from current identity.
// NID never comes from RunosNID alone, and can outlive the current node record.
type EvictionTarget struct {
	Outcome            EvictionOutcome  `json:"outcome"`
	Hostname           *string          `json:"hostname"`
	NID                *string          `json:"nid"`
	RunosNodeName      string           `json:"runosNodeName"`
	RunosNID           string           `json:"runosNid"`
	Source             *EvictionSource  `json:"source"`
	CandidateNIDs      []string         `json:"candidateNids"`
	UnavailableSources []EvictionSource `json:"unavailableSources"`
}

// ReadEvictionTarget reads the resolution that the eviction POST also uses.
// Separate requests share resolution rules, but do not reserve a target.
func (c *Client) ReadEvictionTarget(accountID, clusterID, nodeID, hostname, token string) (*EvictionTarget, error) {
	if accountID == "" || clusterID == "" {
		return nil, fmt.Errorf("account and cluster IDs are required")
	}
	if (nodeID == "") == (hostname == "") {
		return nil, fmt.Errorf("pass exactly one of node ID or hostname")
	}
	query := url.Values{}
	if nodeID != "" {
		query.Set("nid", nodeID)
	} else {
		query.Set("hostname", hostname)
	}
	path := "/" + url.PathEscape(accountID) + "/" + url.PathEscape(clusterID) +
		"/storage-groups/evict-node-target?" + query.Encode()
	result, err := c.Do(http.MethodGet, path, token, nil)
	if err != nil {
		return nil, err
	}
	if result.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("could not resolve the storage target (HTTP %d)", result.StatusCode)
	}
	target, err := decodeEvictionTarget(result.Body)
	if err != nil {
		return nil, err
	}
	if hostname != "" && (target.Hostname == nil || *target.Hostname != hostname) {
		return nil, fmt.Errorf("storage target response does not match the requested hostname")
	}
	return target, nil
}

func decodeEvictionTarget(body []byte) (*EvictionTarget, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, fmt.Errorf("invalid storage target response: %w", err)
	}
	// Missing or null identity strings must not become evidence of absence.
	for _, key := range []string{"outcome", "hostname", "nid", "runosNodeName", "runosNid", "source", "candidateNids", "unavailableSources"} {
		raw, ok := fields[key]
		if !ok || (string(raw) == "null" && key != "hostname" && key != "nid" && key != "source") {
			return nil, fmt.Errorf("invalid storage target field %s", key)
		}
	}
	var target EvictionTarget
	if err := json.Unmarshal(body, &target); err != nil {
		return nil, fmt.Errorf("invalid storage target response: %w", err)
	}
	if err := target.validate(); err != nil {
		return nil, err
	}
	return &target, nil
}

func (t *EvictionTarget) validate() error {
	invalid := fmt.Errorf("inconsistent storage target response")
	for _, value := range []*string{t.NID, t.Hostname} {
		if value != nil && !safeTargetFact(*value) {
			return invalid
		}
	}
	if t.RunosNID != "" && !safeTargetFact(t.RunosNID) {
		return invalid
	}
	if t.RunosNID == "" && t.RunosNodeName != "" {
		return invalid
	}
	var source EvictionSource
	if t.Source != nil {
		source = *t.Source
		if source != EvictionDevices && source != EvictionNodes {
			return invalid
		}
	}
	if (t.NID != nil) != (source == EvictionDevices) {
		return invalid
	}
	unavailable := map[EvictionSource]bool{}
	for _, value := range t.UnavailableSources {
		if (value != EvictionDevices && value != EvictionNodes) || unavailable[value] {
			return invalid
		}
		unavailable[value] = true
	}
	if unavailable[EvictionDevices] && t.NID != nil || unavailable[EvictionNodes] && t.RunosNID != "" {
		return invalid
	}
	candidates := map[string]bool{}
	for _, value := range t.CandidateNIDs {
		if !safeTargetFact(value) || candidates[value] {
			return invalid
		}
		candidates[value] = true
	}
	if t.Outcome != EvictionAmbiguous && len(candidates) != 0 {
		return invalid
	}
	if t.Outcome != EvictionUnknown && t.Outcome != EvictionAmbiguous && len(unavailable) != 0 {
		return invalid
	}
	switch t.Outcome {
	case EvictionMatched:
		if t.RunosNID == "" || t.Hostname == nil || source == "" || (t.NID != nil && *t.NID != t.RunosNID) {
			return invalid
		}
	case EvictionNoRecord:
		if t.RunosNID != "" || t.Hostname == nil || source == EvictionNodes {
			return invalid
		}
	case EvictionUnknown:
		if len(unavailable) == 0 {
			return invalid
		}
	case EvictionAmbiguous:
		if len(candidates) < 2 || t.NID != nil || t.RunosNID != "" || t.Hostname == nil {
			return invalid
		}
	case EvictionMissingHostname:
		if t.Hostname != nil {
			return invalid
		}
	default:
		return fmt.Errorf("unknown storage target outcome")
	}
	return nil
}

// Technical facts remain verbatim, but cannot inject terminal control sequences.
func safeTargetFact(value string) bool {
	return strings.TrimSpace(value) != "" && !strings.ContainsFunc(value, unicode.IsControl)
}
