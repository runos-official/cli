//go:build windows

package vpn

import "fmt"

// The Windows service installer is V8. Until it lands `runos vpn install` on Windows refuses with
// a message naming the OS, so the command compiles and runs everywhere.

// NewService returns the OS-specific VPN service installer.
func NewService() service { return windowsService{} }

type windowsService struct{}

func errWindowsService() error {
	return fmt.Errorf("the RunOS VPN service is not available on Windows yet (macOS first; Linux and Windows follow)")
}

func (windowsService) Install(string, string) error { return errWindowsService() }
func (windowsService) Uninstall() error             { return errWindowsService() }
func (windowsService) Running() (bool, error)       { return false, nil }
func (windowsService) Describe() string {
	return "The RunOS VPN service installer is not available on Windows yet."
}
