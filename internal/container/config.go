// Package container provides the core data structures and lifecycle management
// for lightweight Linux containers. It handles container creation, state tracking,
// and coordination between subsystems (namespaces, cgroups, filesystem, networking).
package container

import (
	"fmt"
	"time"
)

// Default resource limits applied when user doesn't specify constraints.
// These are intentionally conservative to prevent runaway containers.
const (
	DefaultMemoryLimit = 256 * 1024 * 1024 // 256 MB
	DefaultCPUQuota    = 100000             // 100ms per period (100% of 1 core)
	DefaultCPUPeriod   = 100000             // 100ms
	DefaultPidsLimit   = 64                 // Max 64 processes inside container
)

// State represents the lifecycle state of a container.
// Follows the OCI runtime spec state machine:
//
//	creating -> created -> running -> stopped
type State string

const (
	StateCreating State = "creating"
	StateCreated  State = "created"
	StateRunning  State = "running"
	StateStopped  State = "stopped"
)

// NetworkConfig defines the network settings for a container.
// We create a veth pair and attach one end to a bridge on the host,
// and the other end inside the container's network namespace.
type NetworkConfig struct {
	// EnableNetworking controls whether to set up a veth pair and bridge.
	EnableNetworking bool `json:"enable_networking"`

	// BridgeName is the host bridge interface (default: "cage0").
	BridgeName string `json:"bridge_name"`

	// ContainerIP is the IPv4 address assigned to the container (CIDR notation).
	ContainerIP string `json:"container_ip"`

	// GatewayIP is the bridge/gateway IP on the host side.
	GatewayIP string `json:"gateway_ip"`

	// Subnet is the network subnet (e.g., "10.10.0.0/24").
	Subnet string `json:"subnet"`

	// VethHostName is the host-side veth interface name.
	VethHostName string `json:"veth_host_name"`

	// VethContainerName is the container-side veth interface name.
	VethContainerName string `json:"veth_container_name"`

	// EnablePortMapping controls whether to set up iptables DNAT rules.
	EnablePortMapping bool `json:"enable_port_mapping"`

	// PortMappings maps host ports to container ports (e.g., "8080:80").
	PortMappings []PortMapping `json:"port_mappings,omitempty"`
}

// PortMapping represents a host:container port mapping.
type PortMapping struct {
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
	Protocol      string `json:"protocol"` // "tcp" or "udp"
}

// ResourceConfig defines cgroup-based resource constraints.
type ResourceConfig struct {
	// MemoryLimitBytes is the hard memory limit in bytes.
	// Container OOM-killed if exceeded.
	MemoryLimitBytes int64 `json:"memory_limit_bytes"`

	// CPUQuota is the allowed CPU time in microseconds per CPUPeriod.
	// Example: 50000/100000 = 50% of one CPU core.
	CPUQuota int64 `json:"cpu_quota"`

	// CPUPeriod is the scheduling period in microseconds (usually 100000).
	CPUPeriod int64 `json:"cpu_period"`

	// PidsLimit is the maximum number of processes allowed.
	PidsLimit int64 `json:"pids_limit"`
}

// FilesystemConfig defines the overlay filesystem configuration.
type FilesystemConfig struct {
	// RootfsPath is the path to the base root filesystem (lower layer).
	RootfsPath string `json:"rootfs_path"`

	// EnableOverlay controls whether to use OverlayFS.
	// If false, bind-mounts rootfs directly (changes persist).
	EnableOverlay bool `json:"enable_overlay"`
}

// Config holds the complete configuration for creating a container.
type Config struct {
	// ID is the unique identifier for this container (UUID).
	ID string `json:"id"`

	// Name is the human-readable name (auto-generated if empty).
	Name string `json:"name"`

	// Command is the entrypoint command + args to run inside the container.
	Command []string `json:"command"`

	// Environment variables passed into the container.
	Env []string `json:"env"`

	// Hostname set inside the container's UTS namespace.
	Hostname string `json:"hostname"`

	// Resources defines cgroup constraints.
	Resources ResourceConfig `json:"resources"`

	// Network defines networking configuration.
	Network NetworkConfig `json:"network"`

	// Filesystem defines the overlay/rootfs configuration.
	Filesystem FilesystemConfig `json:"filesystem"`
}

// ContainerState holds runtime state persisted to disk.
// This allows `cagectl list` to show running containers across CLI invocations.
type ContainerState struct {
	// ID is the container's unique identifier.
	ID string `json:"id"`

	// Name is the human-readable name.
	Name string `json:"name"`

	// PID is the host PID of the container's init process.
	PID int `json:"pid"`

	// Status is the current lifecycle state.
	Status State `json:"status"`

	// Config is the full configuration used to create this container.
	Config Config `json:"config"`

	// CreatedAt is when the container was created.
	CreatedAt time.Time `json:"created_at"`

	// StartedAt is when the container started running.
	StartedAt *time.Time `json:"started_at,omitempty"`

	// FinishedAt is when the container stopped.
	FinishedAt *time.Time `json:"finished_at,omitempty"`

	// ExitCode is the exit code of the container's init process.
	ExitCode *int `json:"exit_code,omitempty"`

	// CgroupPath is the filesystem path to this container's cgroup.
	CgroupPath string `json:"cgroup_path"`

	// OverlayMountPath is where the overlay filesystem is mounted.
	OverlayMountPath string `json:"overlay_mount_path"`

	// OverlayUpperDir is the upper (writable) layer of overlayfs.
	OverlayUpperDir string `json:"overlay_upper_dir"`

	// OverlayWorkDir is the work directory required by overlayfs.
	OverlayWorkDir string `json:"overlay_work_dir"`
}

// NewDefaultConfig returns a Config with sensible defaults.
func NewDefaultConfig() Config {
	return Config{
		Env:      []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "TERM=xterm"},
		Hostname: "cage",
		Resources: ResourceConfig{
			MemoryLimitBytes: DefaultMemoryLimit,
			CPUQuota:         DefaultCPUQuota,
			CPUPeriod:        DefaultCPUPeriod,
			PidsLimit:        DefaultPidsLimit,
		},
		Network: NetworkConfig{
			EnableNetworking:  true,
			BridgeName:        "cage0",
			Subnet:            "10.10.0.0/24",
			GatewayIP:         "10.10.0.1",
			VethContainerName: "eth0",
		},
		Filesystem: FilesystemConfig{
			EnableOverlay: true,
		},
	}
}

// Validate checks that the config has all required fields.
func (c *Config) Validate() error {
	if len(c.Command) == 0 {
		return fmt.Errorf("container command cannot be empty")
	}
	if c.Filesystem.RootfsPath == "" {
		return fmt.Errorf("rootfs path is required")
	}
	if c.Resources.MemoryLimitBytes <= 0 {
		return fmt.Errorf("memory limit must be positive")
	}
	if c.Resources.CPUQuota <= 0 || c.Resources.CPUPeriod <= 0 {
		return fmt.Errorf("CPU quota and period must be positive")
	}
	if c.Resources.PidsLimit <= 0 {
		return fmt.Errorf("pids limit must be positive")
	}
	return nil
}
