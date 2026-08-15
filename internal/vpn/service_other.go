//go:build !darwin

package vpn

import (
	"fmt"
	"runtime"
)

// The Linux (systemd) and Windows (service) installers are V5 step 4 and V8. Until they land the
// installer refuses with a message naming the OS, so `runos vpn install` compiles and runs
// everywhere but only does the real thing on macOS.

// NewService returns the OS-specific VPN service installer.
func NewService() service { return unsupportedService{} }

type unsupportedService struct{}

func (unsupportedService) Install(string, string) error { return errUnsupportedService() }
func (unsupportedService) Uninstall() error             { return errUnsupportedService() }
func (unsupportedService) Running() (bool, error)       { return false, nil }
func (unsupportedService) Describe(string) string {
	return "the RunOS VPN service installer is macOS-only for now"
}

func errUnsupportedService() error {
	return fmt.Errorf("the RunOS VPN service is not available on %s yet (macOS first; Linux and Windows follow)", runtime.GOOS)
}
