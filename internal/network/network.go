// Package network configures container networking using veth pairs and bridges.
//
// Container networking overview:
//
//	┌─────────────────────────────────────────────────────────────┐
//	│ HOST                                                        │
//	│                                                             │
//	│  ┌─────────┐     ┌──────────────┐     ┌─────────────────┐  │
//	│  │  eth0   │────▶│  cage0       │────▶│  vethXXXX       │  │
//	│  │ (host)  │     │  (bridge)    │     │  (host side)    │  │
//	│  └─────────┘     │  10.10.0.1   │     └────────┬────────┘  │
//	│                  └──────────────┘              │            │
//	│                                                │            │
//	│  ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ network namespace ─ ─ │─ ─ ─ ─ ─  │
//	│                                                │            │
//	│  ┌──────────────────────────────────────────┐  │            │
//	│  │ CONTAINER                                │  │            │
//	│  │                                          │  │            │
//	│  │  ┌────────────────┐                      │  │            │
//	│  │  │  eth0          │◀─────────────────────┘  │            │
//	│  │  │  (container)   │                         │            │
//	│  │  │  10.10.0.2     │                         │            │
//	│  │  └────────────────┘                         │            │
//	│  │                                             │            │
//	│  └─────────────────────────────────────────────┘            │
//	└─────────────────────────────────────────────────────────────┘
//
// How it works:
//  1. Create a bridge (cage0) on the host — acts like a virtual switch
//  2. Create a veth pair — two virtual NICs connected like a pipe
//  3. Attach one end (vethXXXX) to the bridge on the host
//  4. Move the other end (eth0) into the container's network namespace
//  5. Assign IP addresses and set up routes
//  6. Enable IP forwarding + NAT so containers can reach the internet
package network

import (
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

// Manager handles network setup and teardown for a container.
type Manager struct {
	ContainerID string

	// Bridge configuration
	BridgeName string
	BridgeIP   string // CIDR notation, e.g., "10.10.0.1/24"
	Subnet     string // e.g., "10.10.0.0/24"

	// Veth pair names
	VethHost      string // Host-side veth name (auto-generated)
	VethContainer string // Container-side veth name (typically "eth0")

	// Container IP
	ContainerIP string // CIDR notation, e.g., "10.10.0.2/24"
	GatewayIP   string // e.g., "10.10.0.1"
}

// NewManager creates a network manager with the given configuration.
func NewManager(containerID, bridgeName, containerIP, gatewayIP, subnet, vethContainer string) *Manager {
	// Generate a short random suffix for the host veth name.
	// This avoids collisions when multiple containers are running.
	suffix := randomHex(4)
	vethHost := fmt.Sprintf("veth%s", suffix)

	// Ensure container IP has CIDR notation
	if !strings.Contains(containerIP, "/") {
		containerIP = containerIP + "/24"
	}

	return &Manager{
		ContainerID:   containerID,
		BridgeName:    bridgeName,
		BridgeIP:      gatewayIP + "/24",
		Subnet:        subnet,
		VethHost:      vethHost,
		VethContainer: vethContainer,
		ContainerIP:   containerIP,
		GatewayIP:     gatewayIP,
	}
}

// SetupBridge creates the bridge interface on the host if it doesn't exist.
//
// A bridge is like a virtual network switch. All container veth pairs connect
// to it, allowing containers to communicate with each other and the host.
func (m *Manager) SetupBridge() error {
	// Check if bridge already exists
	br, err := netlink.LinkByName(m.BridgeName)
	if err == nil {
		// Bridge exists, make sure it's up
		return netlink.LinkSetUp(br)
	}

	// Create new bridge
	la := netlink.NewLinkAttrs()
	la.Name = m.BridgeName
	bridge := &netlink.Bridge{LinkAttrs: la}

	if err := netlink.LinkAdd(bridge); err != nil {
		return fmt.Errorf("failed to create bridge %s: %w", m.BridgeName, err)
	}

	// Assign IP address to the bridge
	addr, err := netlink.ParseAddr(m.BridgeIP)
	if err != nil {
		return fmt.Errorf("failed to parse bridge IP %s: %w", m.BridgeIP, err)
	}

	br, _ = netlink.LinkByName(m.BridgeName)
	if err := netlink.AddrAdd(br, addr); err != nil {
		// Address might already exist if bridge was partially set up
		if !os.IsExist(err) && !strings.Contains(err.Error(), "exists") {
			return fmt.Errorf("failed to assign IP to bridge: %w", err)
		}
	}

	// Bring the bridge up
	if err := netlink.LinkSetUp(br); err != nil {
		return fmt.Errorf("failed to bring up bridge: %w", err)
	}

	return nil
}

// SetupVethPair creates a veth pair and connects one end to the bridge.
//
// A veth (virtual ethernet) pair is two virtual NICs connected by a pipe.
// Whatever goes in one end comes out the other. We attach one end to the
// host bridge and move the other end into the container's network namespace.
func (m *Manager) SetupVethPair(containerPID int) error {
	// Create the veth pair
	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{
			Name: m.VethHost,
		},
		PeerName: m.VethContainer,
	}

	if err := netlink.LinkAdd(veth); err != nil {
		return fmt.Errorf("failed to create veth pair (%s <-> %s): %w",
			m.VethHost, m.VethContainer, err)
	}

	// Get the bridge interface
	br, err := netlink.LinkByName(m.BridgeName)
	if err != nil {
		return fmt.Errorf("bridge %s not found: %w", m.BridgeName, err)
	}

	// Attach host-side veth to the bridge
	hostVeth, err := netlink.LinkByName(m.VethHost)
	if err != nil {
		return fmt.Errorf("failed to find host veth %s: %w", m.VethHost, err)
	}

	if err := netlink.LinkSetMaster(hostVeth, br.(*netlink.Bridge)); err != nil {
		return fmt.Errorf("failed to attach veth to bridge: %w", err)
	}

	// Bring up the host-side veth
	if err := netlink.LinkSetUp(hostVeth); err != nil {
		return fmt.Errorf("failed to bring up host veth: %w", err)
	}

	// Move the container-side veth into the container's network namespace.
	// After this, the interface disappears from the host and appears inside
	// the container's network namespace.
	containerVeth, err := netlink.LinkByName(m.VethContainer)
	if err != nil {
		return fmt.Errorf("failed to find container veth %s: %w", m.VethContainer, err)
	}

	if err := netlink.LinkSetNsPid(containerVeth, containerPID); err != nil {
		return fmt.Errorf("failed to move veth into container namespace (PID %d): %w",
			containerPID, err)
	}

	return nil
}

// ConfigureContainerNetwork sets up networking inside the container's namespace.
//
// This function enters the container's network namespace to:
//  1. Assign an IP address to the container's veth interface
//  2. Bring up the loopback (lo) and eth0 interfaces
//  3. Set the default route through the bridge gateway
//
// We use runtime.LockOSThread() because network namespaces are per-thread
// in Linux, and Go's goroutine scheduler can move goroutines between threads.
func (m *Manager) ConfigureContainerNetwork(containerPID int) error {
	// Lock this goroutine to its current OS thread.
	// Network namespace changes are per-thread, and without this lock,
	// Go might schedule another goroutine on this thread while we're
	// in the container's namespace — causing that goroutine to run
	// in the wrong namespace.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Save our current (host) network namespace so we can return to it
	hostNS, err := netns.Get()
	if err != nil {
		return fmt.Errorf("failed to get host network namespace: %w", err)
	}
	defer hostNS.Close()

	// Enter the container's network namespace
	containerNS, err := netns.GetFromPid(containerPID)
	if err != nil {
		return fmt.Errorf("failed to get container namespace for PID %d: %w", containerPID, err)
	}
	defer containerNS.Close()

	if err := netns.Set(containerNS); err != nil {
		return fmt.Errorf("failed to enter container namespace: %w", err)
	}
	// IMPORTANT: defer switching back to host namespace
	defer func() { _ = netns.Set(hostNS) }()

	// --- Now we're inside the container's network namespace ---

	// Bring up loopback
	lo, err := netlink.LinkByName("lo")
	if err == nil {
		_ = netlink.LinkSetUp(lo)
	}

	// Find and configure the container-side veth
	containerVeth, err := netlink.LinkByName(m.VethContainer)
	if err != nil {
		return fmt.Errorf("container veth %s not found inside namespace: %w",
			m.VethContainer, err)
	}

	// Assign IP address
	addr, err := netlink.ParseAddr(m.ContainerIP)
	if err != nil {
		return fmt.Errorf("failed to parse container IP %s: %w", m.ContainerIP, err)
	}

	if err := netlink.AddrAdd(containerVeth, addr); err != nil {
		return fmt.Errorf("failed to assign IP %s to container: %w", m.ContainerIP, err)
	}

	// Bring up the container's veth interface
	if err := netlink.LinkSetUp(containerVeth); err != nil {
		return fmt.Errorf("failed to bring up container veth: %w", err)
	}

	// Add default route through the gateway (bridge IP on host)
	gateway := net.ParseIP(m.GatewayIP)
	if gateway == nil {
		return fmt.Errorf("invalid gateway IP: %s", m.GatewayIP)
	}

	defaultRoute := &netlink.Route{
		Dst: nil, // nil = default route (0.0.0.0/0)
		Gw:  gateway,
	}
	if err := netlink.RouteAdd(defaultRoute); err != nil {
		return fmt.Errorf("failed to add default route via %s: %w", m.GatewayIP, err)
	}

	return nil
}

// EnableIPForwarding turns on IP forwarding on the host.
// Without this, the host won't forward packets between the bridge and the
// external network, so containers won't be able to reach the internet.
func EnableIPForwarding() error {
	return os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0644)
}

// SetupNAT configures iptables MASQUERADE rules so containers can access
// the internet through the host's network connection.
//
// MASQUERADE rewrites the source IP of outgoing packets from the container's
// IP (10.10.0.x) to the host's external IP, and tracks connections to
// rewrite response packets back.
func SetupNAT(subnet, bridgeName string) error {
	// Check if rule already exists
	checkCmd := exec.Command("iptables", "-t", "nat", "-C", "POSTROUTING",
		"-s", subnet, "!", "-o", bridgeName, "-j", "MASQUERADE")
	if checkCmd.Run() == nil {
		return nil // Rule already exists
	}

	// Add MASQUERADE rule
	cmd := exec.Command("iptables", "-t", "nat", "-A", "POSTROUTING",
		"-s", subnet, "!", "-o", bridgeName, "-j", "MASQUERADE")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to set up NAT: %w (output: %s)", err, string(output))
	}

	// Allow forwarding to/from the bridge
	for _, args := range [][]string{
		{"-A", "FORWARD", "-i", bridgeName, "-j", "ACCEPT"},
		{"-A", "FORWARD", "-o", bridgeName, "-j", "ACCEPT"},
	} {
		checkCmd := exec.Command("iptables", append([]string{"-C"}, args[1:]...)...)
		if checkCmd.Run() != nil {
			cmd := exec.Command("iptables", args...)
			if output, err := cmd.CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "warning: iptables rule failed: %s\n", string(output))
			}
		}
	}

	return nil
}

// Cleanup removes the host-side veth interface.
// The container-side is automatically removed when the namespace is destroyed.
func (m *Manager) Cleanup() error {
	link, err := netlink.LinkByName(m.VethHost)
	if err != nil {
		// Already cleaned up
		return nil
	}
	return netlink.LinkDel(link)
}

// AllocateIP assigns the next available IP in the subnet for a new container.
// It reads existing container states to avoid collisions.
func AllocateIP(subnet, gatewayIP string) (string, error) {
	// Parse the subnet to get the base network
	_, ipNet, err := net.ParseCIDR(subnet)
	if err != nil {
		return "", fmt.Errorf("invalid subnet %s: %w", subnet, err)
	}

	// Collect IPs already in use
	usedIPs := make(map[string]bool)
	usedIPs[gatewayIP] = true

	// Start allocating from .2 (since .1 is the gateway)
	ip := make(net.IP, len(ipNet.IP))
	copy(ip, ipNet.IP)

	for i := 2; i < 255; i++ {
		ip[len(ip)-1] = byte(i)
		candidate := ip.String()
		if !usedIPs[candidate] && ipNet.Contains(ip) {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("no available IPs in subnet %s", subnet)
}

// randomHex generates a random hex string of the given number of bytes.
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}
