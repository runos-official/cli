package vpn

import (
	"fmt"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
)

// engine wraps one wireguard-go device on one tun interface. It owns the WireGuard layer only:
// the address on the interface, the routes and the DNS are the platform's job (platform_*.go),
// because those are the parts that differ per OS.
//
// The engine converges: every apply sends the WHOLE peer set through the UAPI with replace
// semantics, so a peer no longer in the plan disappears. Same rule as the wg1 servers, for the
// same reason a declaration that can be turned on but not off is not a declaration.
type engine struct {
	dev    *device.Device
	tun    tun.Device
	name   string
	logger *device.Logger
}

// newEngine creates the tun interface and the wireguard-go device. On macOS the name must be
// "utun" (the kernel picks the number) or "utunN"; the caller reads the assigned name back with
// InterfaceName. Requires root: creating a network interface does on every OS (decision 5).
func newEngine(requestedName string, verbose bool) (*engine, error) {
	level := device.LogLevelError
	if verbose {
		level = device.LogLevelVerbose
	}
	logger := device.NewLogger(level, "runos-vpn: ")

	tunDev, err := tun.CreateTUN(requestedName, defaultMTU)
	if err != nil {
		return nil, fmt.Errorf("create tun interface: %w", err)
	}
	name, err := tunDev.Name()
	if err != nil {
		_ = tunDev.Close()
		return nil, fmt.Errorf("read tun name: %w", err)
	}
	dev := device.NewDevice(tunDev, conn.NewDefaultBind(), logger)
	return &engine{dev: dev, tun: tunDev, name: name, logger: logger}, nil
}

// defaultMTU leaves room for the WireGuard header over a 1500-byte path (1420 is WireGuard's own
// default for IPv4 over IPv4).
const defaultMTU = 1420

// InterfaceName is the OS interface name the tun got (e.g. "utun6"), needed to add routes and to
// address the interface.
func (e *engine) InterfaceName() string { return e.name }

// Up brings the device up so it starts handshaking.
func (e *engine) Up() error {
	if err := e.dev.Up(); err != nil {
		return fmt.Errorf("bring wireguard device up: %w", err)
	}
	return nil
}

// ApplyPlan configures the private key and the full peer set from a plan.
func (e *engine) ApplyPlan(privateKeyHex string, plan Plan) error {
	if err := e.dev.IpcSet(RenderUAPISet(privateKeyHex, plan)); err != nil {
		return fmt.Errorf("apply wireguard configuration: %w", err)
	}
	return nil
}

// Stats reads the current per-peer statistics, keyed by public key (hex).
func (e *engine) Stats() (map[string]*PeerStats, error) {
	dump, err := e.dev.IpcGet()
	if err != nil {
		return nil, fmt.Errorf("read wireguard state: %w", err)
	}
	return ParseUAPIGet(dump)
}

// Close tears the device and interface down.
func (e *engine) Close() {
	if e.dev != nil {
		e.dev.Close()
	}
}
