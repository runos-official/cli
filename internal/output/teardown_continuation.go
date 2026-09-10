package output

import (
	"fmt"
	"io"
	"strings"
)

func renderTeardownContinuation(writer io.Writer, envelope, read map[string]any) {
	cursor := stringValue(envelope["teardownNextCursor"])
	if cursor == "" {
		cursor = stringValue(envelope["nextCursor"])
	}

	jobID := stringValue(read["jobId"])
	cid := stringValue(read["cid"])
	if jobID == "" {
		if cursor != "" {
			fmt.Fprintf(writer, "Continue the same history command with --cursor %s; keep all existing filters and scope.\n", shellArgument(cursor))
		}
		return
	}

	fmt.Fprintf(writer, "Read job outcomes: runos node-teardowns list --job-id %s", jobID)
	if cursor != "" {
		fmt.Fprintf(writer, " --cursor %s", shellArgument(cursor))
	}
	fmt.Fprintf(writer, "%s\n", clusterArgument(cid))
}

func clusterArgument(cid string) string {
	if cid == "" {
		return ""
	}
	return " --cid " + cid
}

func shellArgument(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
