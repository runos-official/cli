package cmd

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

func openBrowser(url string) error {
	// Only allow HTTPS URLs (or http://localhost for development)
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://localhost") {
		return fmt.Errorf("refusing to open non-HTTPS URL: %s", url)
	}

	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}

	return cmd.Start()
}
