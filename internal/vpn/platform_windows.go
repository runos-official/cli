//go:build windows

package vpn

import (
	"fmt"
	"net/netip"
)

// The Windows platform (wireguard-go over wintun, routes via the IP helper API, NRPT for split
// DNS) is V5 step 4 / V8. Until it lands the daemon builds and runs on Windows but refuses to
// bring a tunnel up, with a message naming the OS rather than a cryptic failure.

const defaultTunName = "runos0"

type windowsPlatform struct{}

func newPlatform() platform { return windowsPlatform{} }

func errWindowsUnsupported() error {
	return fmt.Errorf("the RunOS VPN client is not available on Windows yet (macOS first; Linux and Windows follow)")
}

func (windowsPlatform) SetInterfaceAddress(string, netip.Prefix) error {
	return errWindowsUnsupported()
}
func (windowsPlatform) Routes(string) ([]netip.Prefix, error)  { return nil, errWindowsUnsupported() }
func (windowsPlatform) AddRoute(string, netip.Prefix) error    { return errWindowsUnsupported() }
func (windowsPlatform) RemoveRoute(string, netip.Prefix) error { return errWindowsUnsupported() }
func (windowsPlatform) Resolvers() (map[string]netip.Addr, error) {
	return nil, errWindowsUnsupported()
}
func (windowsPlatform) SetResolver(string, netip.Addr) error { return errWindowsUnsupported() }
func (windowsPlatform) RemoveResolver(string) error          { return errWindowsUnsupported() }
func (windowsPlatform) FlushDNS() error                      { return errWindowsUnsupported() }
func (windowsPlatform) Teardown(string) error                { return errWindowsUnsupported() }
