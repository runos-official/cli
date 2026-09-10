package output

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/runos-official/cli/internal/manifest"
)

// formatTeardownEnvelope keeps ordinary response fields beside complete outcome blocks.
func (f *Formatter) formatTeardownEnvelope(data []byte, definition *manifest.Output) (bool, error) {
	envelope, ok := decodeJSONObject(data)
	if !ok {
		return false, nil
	}

	var outcomes strings.Builder
	changed := false
	if rows, ok := envelope["jobs"].([]any); ok {
		for _, raw := range rows {
			row, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			block, cleaned := separateTeardownFields(row)
			changed = changed || cleaned
			if block != "" {
				fmt.Fprintln(&outcomes)
				for _, field := range []struct{ label, key string }{{"Job", "id"}, {"Type", "type"}, {"Status", "status"}, {"Error", "error"}} {
					writeTeardownField(&outcomes, field.label, stringValue(row[field.key]))
				}
				outcomes.WriteString(block)
			}
		}
	} else {
		block, cleaned := separateTeardownFields(envelope)
		changed = cleaned
		outcomes.WriteString(block)
	}
	if !changed {
		return false, nil
	}
	if len(envelope) > 0 {
		ordinary, err := json.Marshal(envelope)
		if err != nil {
			return true, err
		}
		if err := f.Format(ordinary, definition); err != nil {
			return true, err
		}
	}
	fmt.Fprint(os.Stdout, outcomes.String())
	return true, nil
}

func separateTeardownFields(envelope map[string]any) (string, bool) {
	if !validTeardownMetadata(envelope) {
		return "", false
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return "", false
	}
	var block strings.Builder
	rendered := RenderTeardowns(&block, data)
	if isTeardownRecord(envelope) {
		clear(envelope)
		return block.String(), true
	}
	if raw, present := envelope["teardown"]; present {
		record, ok := raw.(map[string]any)
		if !ok || !isTeardownRecord(record) {
			return "", false
		}
		delete(envelope, "teardown")
		return block.String(), true
	}
	if raw, present := envelope["teardowns"]; present {
		if _, valid := teardownRecords(raw); !valid {
			return "", false
		}
	}
	// Empty metadata is standard on unrelated cluster jobs and has no text outcome.
	changed := false
	for _, key := range []string{"teardowns", "teardownRead", "teardownReadError", "teardownNextCursor"} {
		if _, present := envelope[key]; present {
			delete(envelope, key)
			changed = true
		}
	}
	if changed && rendered {
		delete(envelope, "nextCursor")
	}
	return block.String(), changed
}

func validTeardownMetadata(envelope map[string]any) bool {
	if raw, present := envelope["teardowns"]; present {
		if _, ok := raw.([]any); !ok {
			return false
		}
		if _, valid := teardownRecords(raw); !valid {
			return false
		}
	}
	if raw := envelope["teardownRead"]; raw != nil {
		if _, ok := raw.(map[string]any); !ok {
			return false
		}
	}
	for _, key := range []string{"teardownReadError", "teardownNextCursor"} {
		if raw := envelope[key]; raw != nil {
			if _, ok := raw.(string); !ok {
				return false
			}
		}
	}
	return true
}
