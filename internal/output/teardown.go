package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// SafeNodeName returns a terminal-safe node name.
func SafeNodeName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}
	for _, character := range trimmed {
		if unicode.IsControl(character) {
			return ""
		}
	}
	return trimmed
}

// RenderTeardowns renders teardown records found in an API response.
func RenderTeardowns(writer io.Writer, data []byte) bool {
	envelope, ok := decodeJSONObject(data)
	if !ok {
		return false
	}

	if record, ok := teardownRecord(envelope); ok {
		renderTeardownRecord(writer, record)
		return true
	}

	if raw, present := envelope["teardown"]; present {
		record, ok := raw.(map[string]any)
		if !ok || !isTeardownRecord(record) {
			return false
		}
		renderTeardownRecord(writer, record)
		return true
	}
	if !validTeardownMetadata(envelope) {
		return false
	}

	rawRecords, recordsPresent := envelope["teardowns"]
	read, readPresent := envelope["teardownRead"].(map[string]any)
	readError := envelope["teardownReadError"]
	if !recordsPresent && !readPresent && stringValue(readError) == "" {
		return false
	}

	records, recordsValid := teardownRecords(rawRecords)
	if recordsPresent && !recordsValid {
		return false
	}
	readErrorMessage := stringValue(readError)
	_, historyPage := envelope["nextCursor"]
	if len(records) == 0 && readErrorMessage == "" && !(historyPage && recordsPresent) && !(readPresent && isTeardownJob(envelope)) {
		return false
	}
	if len(records) == 0 {
		if readErrorMessage != "" {
			fmt.Fprintf(writer, "Teardown outcomes are unavailable: %s\n", readErrorMessage)
		} else if !recordsPresent {
			fmt.Fprintln(writer, "Teardown outcome metadata is unavailable.")
		} else {
			fmt.Fprintln(writer, "No teardown records are available yet.")
		}
	} else {
		for index, record := range records {
			if index > 0 {
				fmt.Fprintln(writer)
			}
			renderTeardownRecord(writer, record)
		}
		if readErrorMessage != "" {
			fmt.Fprintf(writer, "\nAdditional teardown outcomes are unavailable: %s\n", readErrorMessage)
		}
	}

	renderTeardownContinuation(writer, envelope, read)
	return true
}

// RenderJobReference prints the read command for an accepted queued job.
func RenderJobReference(writer io.Writer, data []byte) bool {
	envelope, ok := decodeJSONObject(data)
	if !ok {
		return false
	}
	jobID := stringValue(envelope["jobId"])
	if jobID == "" {
		return false
	}
	for _, readField := range []string{"status", "workItems", "logs", "teardownRead"} {
		if _, present := envelope[readField]; present {
			return false
		}
	}
	fmt.Fprintln(writer, "Work accepted.")
	fmt.Fprintf(writer, "Read job: runos jobs show %s\n", jobID)
	return true
}

// TeardownFingerprint returns visible teardown output for follow deduplication.
func TeardownFingerprint(data []byte) string {
	var rendered strings.Builder
	if !RenderTeardowns(&rendered, data) {
		return ""
	}
	return rendered.String()
}

func isTeardownJob(envelope map[string]any) bool {
	switch stringValue(envelope["type"]) {
	case "cluster.reset", "nodes.deleteAndReset", "cloudProviders.removeServer":
		return true
	default:
		// Provisioning only becomes teardown-related when rollback creates a record.
		return false
	}
}

func decodeJSONObject(data []byte) (map[string]any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return nil, false
	}
	return object, object != nil
}

func teardownRecord(object map[string]any) (map[string]any, bool) {
	if !isTeardownRecord(object) {
		return nil, false
	}
	return object, true
}

func isTeardownRecord(record map[string]any) bool {
	required := []string{"id", "nid", "operationKind", "acknowledgementApplicable", "acceptanceState", "providerState"}
	for _, field := range required {
		if _, present := record[field]; !present {
			return false
		}
	}
	return true
}

func teardownRecords(raw any) ([]map[string]any, bool) {
	if raw == nil {
		return nil, true
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	records := make([]map[string]any, 0, len(values))
	for _, value := range values {
		record, ok := value.(map[string]any)
		if !ok || !isTeardownRecord(record) {
			return nil, false
		}
		records = append(records, record)
	}
	return records, true
}

func renderTeardownRecord(writer io.Writer, record map[string]any) {
	fmt.Fprintln(writer, acceptanceMessage(stringValue(record["acceptanceState"])))
	fmt.Fprintf(writer, "Agent acknowledgement: %s\n", acknowledgementMessage(record))

	name := SafeNodeName(stringValue(record["name"]))
	if name != "" {
		fmt.Fprintf(writer, "Node: %s\n", name)
	}
	writeTeardownField(writer, "Node id", stringValue(record["nid"]))
	writeTeardownField(writer, "Teardown id", stringValue(record["id"]))
	writeTeardownField(writer, "Operation", stringValue(record["operationKind"]))
	writeTeardownField(writer, "Acceptance state", stringValue(record["acceptanceState"]))
	if state := stringValue(record["state"]); state != "" {
		writeTeardownField(writer, "Acknowledgement state", state)
	}
	writeTeardownField(writer, "Reason code", stringValue(record["reasonCode"]))
	writeTeardownField(writer, "Reason", stringValue(record["reason"]))
	writeTeardownField(writer, "Remedy", teardownRemedy(record))

	providerState := stringValue(record["providerState"])
	if providerState != "" {
		fmt.Fprintf(writer, "Provider destruction: %s\n", providerMessage(providerState))
	}
	writeTeardownField(writer, "Provider reason", stringValue(record["providerReason"]))
	writeTeardownField(writer, "Provider remedy", stringValue(record["providerRemedy"]))

	for _, field := range []struct {
		label string
		key   string
	}{
		{"Requested at", "requestedAt"},
		{"Dispatched at", "dispatchedAt"},
		{"Resolved at", "resolvedAt"},
		{"Updated at", "updatedAt"},
		{"Tracking deadline", "trackingDeadlineAt"},
	} {
		writeTeardownField(writer, field.label, stringValue(record[field.key]))
	}

	id := stringValue(record["id"])
	cid := stringValue(record["cid"])
	if id != "" {
		fmt.Fprintf(writer, "Read later: runos node-teardowns show %s%s\n", id, clusterArgument(cid))
	}
}

func acceptanceMessage(state string) string {
	switch state {
	case "committed":
		return "Intent recorded. Deletion acceptance is not established."
	case "accepted":
		return "Deletion accepted."
	case "refused":
		return "Deletion refused."
	case "unknown":
		return "Deletion acceptance is uncertain."
	default:
		if state == "" {
			return "Deletion acceptance is unavailable."
		}
		return "Deletion acceptance: " + state + "."
	}
}

func acknowledgementMessage(record map[string]any) string {
	applicable, _ := record["acknowledgementApplicable"].(bool)
	if !applicable {
		return "Uninstall acknowledgement was not requested."
	}
	switch stringValue(record["state"]) {
	case "pending":
		return "Scheduling acknowledgement remains pending."
	case "acknowledged":
		return "The agent acknowledged scheduling uninstall. Uninstall and reboot completion are not established."
	case "unacknowledged":
		return "Positive acknowledgement was not obtained."
	case "unknown":
		return "Tracking cannot establish the agent outcome."
	case "":
		return "Acknowledgement evidence is unavailable."
	default:
		return "Unknown state: " + stringValue(record["state"]) + "."
	}
}

func providerMessage(state string) string {
	switch state {
	case "not_requested":
		return "Not requested."
	case "pending":
		return "Pending."
	case "confirmed_destroyed":
		return "Confirmed destroyed."
	case "failed":
		return "Failed."
	case "unknown":
		return "Unknown."
	default:
		return state + "."
	}
}

func writeTeardownField(writer io.Writer, label, value string) {
	if value == "" {
		return
	}
	lines := strings.Split(value, "\n")
	fmt.Fprintf(writer, "%s: %s\n", label, lines[0])
	for _, line := range lines[1:] {
		fmt.Fprintf(writer, "  %s\n", line)
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
