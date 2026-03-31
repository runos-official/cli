package jobs

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"golang.org/x/term"
)

const (
	maxResultLength = 80
)

var spinnerChars = []string{"-", "\\", "|", "/"}

// DisplayStatus prints a summary of the job's current status to stdout.
func DisplayStatus(job *JobStatus) {
	fmt.Printf("Job:      %s\n", job.ID)
	fmt.Printf("Type:     %s\n", job.Type)
	fmt.Printf("Status:   %s\n", job.Status)
	fmt.Printf("Progress: %s\n", job.Progress)

	if job.Error != "" {
		fmt.Printf("Error:    %s\n", job.Error)
	}
}

// DisplayFollow renders the follow-mode display with job status and work item progress.
func DisplayFollow(job *JobStatus, items []WorkItem, spinnerIdx int, isFinal bool) {
	// Clear screen and move cursor to top (only when writing to a terminal)
	if term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Print("\033[2J\033[H")
	}

	// Header
	fmt.Printf("Job: %s (%s)\n\n", job.ID, job.Type)

	// Sort work items by step number
	sort.Slice(items, func(i, j int) bool {
		return items[i].StepNumber < items[j].StepNumber
	})

	// Display each work item
	for i := range items {
		item := &items[i]
		icon := getStatusIcon(item.Status, spinnerIdx)
		fmt.Printf("  %s  %s\n", icon, item.Name)

		// Show result if available
		if result := item.Result(); result != "" {
			formatted := formatResult(result, isFinal)
			// Indent result lines
			lines := strings.Split(formatted, "\n")
			for _, line := range lines {
				if line != "" {
					fmt.Printf("          %s\n", line)
				}
			}
		}
	}

	// Footer
	fmt.Println()
	fmt.Printf("Status: %s (%s)\n", job.Status, job.Progress)

	if job.Error != "" {
		fmt.Printf("Error: %s\n", job.Error)
	}
}

// getStatusIcon returns the appropriate icon for a work item status
func getStatusIcon(status string, spinnerIdx int) string {
	switch status {
	case "completed":
		return "[done]"
	case "failed":
		return "[fail]"
	case "in_progress", "running":
		return "[" + spinnerChars[spinnerIdx%len(spinnerChars)] + "]"
	default:
		return "[   ]"
	}
}

// formatResult truncates or formats the result string
func formatResult(result string, showFull bool) string {
	result = strings.TrimSpace(result)

	if showFull {
		return result
	}

	// For in-progress display, truncate long results
	if len(result) > maxResultLength {
		return result[:maxResultLength-3] + "..."
	}

	// Truncate to first line if multi-line
	if idx := strings.Index(result, "\n"); idx > 0 {
		if idx > maxResultLength {
			return result[:maxResultLength-3] + "..."
		}
		return result[:idx] + "..."
	}

	return result
}
