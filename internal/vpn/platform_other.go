//go:build !darwin

package vpn

import (
	"fmt"
	"net/netip"
	"runtime"
)

// The Linux and Windows platforms are V5 step 4 (goal 27). Until they land, the daemon builds on
// every OS (so the release matrix stays green and `runos vpn` exists everywhere) but refuses to
// bring a tunnel up anywhere but macOS, with a message that says so rather than a cryptic failure.

type unsupportedPlatform struct{}

func newPlatform() platform { return unsupportedPlatform{} }

func errUnsupported() error {
	return fmt.Errorf("the RunOS VPN client is not available on %s yet (macOS first; Linux and Windows follow)", runtime.GOOS)
}

func (unsupportedPlatform) SetInterfaceAddress(string, netip.Prefix) error { return errUnsupported() }
func (unsupportedPlatform) Routes(string) ([]netip.Prefix, error)          { return nil, errUnsupported() }
func (unsupportedPlatform) AddRoute(string, netip.Prefix) error            { return errUnsupported() }
func (unsupportedPlatform) RemoveRoute(string, netip.Prefix) error         { return errUnsupported() }
func (unsupportedPlatform) Resolvers() (map[string]netip.Addr, error)      { return nil, errUnsupported() }
func (unsupportedPlatform) SetResolver(string, netip.Addr) error           { return errUnsupported() }
func (unsupportedPlatform) RemoveResolver(string) error                    { return errUnsupported() }
func (unsupportedPlatform) FlushDNS() error                                { return errUnsupported() }
func (unsupportedPlatform) Teardown(string) error                          { return errUnsupported() }
