// Package cgroup manages Linux cgroup v2 resource constraints for containers.
//
// Cgroups (control groups) are the Linux kernel's mechanism for limiting, accounting,
// and isolating resource usage of process groups. While namespaces provide isolation
// of WHAT a process can see, cgroups control HOW MUCH of a resource it can use.
//
// We use cgroup v2 (unified hierarchy) which is the modern interface where all
// controllers are managed through a single filesystem tree at /sys/fs/cgroup.
//
// Resource controls implemented:
//   - Memory: Hard limit via memory.max (OOM kill if exceeded)
//   - CPU: Bandwidth control via cpu.max (quota/period)
//   - PIDs: Process count limit via pids.max (fork bomb protection)
//
// Each container gets its own cgroup at:
//
//	/sys/fs/cgroup/cagectl/<container-id>/
package cgroup

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// CgroupV2Root is the mount point for the unified cgroup v2 hierarchy.
const CgroupV2Root = "/sys/fs/cgroup"

// CagectlCgroupRoot is the parent cgroup for all cagectl containers.
const CagectlCgroupRoot = "/sys/fs/cgroup/cagectl"

// Manager handles cgroup creation, configuration, and cleanup for a container.
type Manager struct {
	// ContainerID is used to create a unique cgroup path.
	ContainerID string

	// Path is the full filesystem path to this container's cgroup.
	// e.g., /sys/fs/cgroup/cagectl/<container-id>
	Path string
}

// NewManager creates a new cgroup manager for the given container ID.
func NewManager(containerID string) *Manager {
	return &Manager{
		ContainerID: containerID,
		Path:        filepath.Join(CagectlCgroupRoot, containerID),
	}
}

// Setup creates the cgroup directory and enables required controllers.
//
// In cgroup v2, controllers must be enabled in the parent's cgroup.subtree_control
// before child cgroups can use them. We write "+memory +cpu +pids" to enable
// the controllers we need.
func (m *Manager) Setup() error {
	// Ensure the parent cagectl cgroup exists
	if err := os.MkdirAll(CagectlCgroupRoot, 0755); err != nil {
		return fmt.Errorf("failed to create cagectl cgroup root: %w", err)
	}

	// Enable controllers in the cagectl parent cgroup.
	// This is required before we can use these controllers in child cgroups.
	subtreeControl := filepath.Join(CagectlCgroupRoot, "cgroup.subtree_control")

	// Check which controllers are available at the root level
	availableControllers, err := m.getAvailableControllers()
	if err != nil {
		return fmt.Errorf("failed to read available controllers: %w", err)
	}

	// Enable each required controller if available
	for _, controller := range []string{"memory", "cpu", "pids"} {
		if contains(availableControllers, controller) {
			// First enable in the root's subtree_control so cagectl cgroup can use it
			rootSubtree := filepath.Join(CgroupV2Root, "cgroup.subtree_control")
			_ = writeFile(rootSubtree, "+"+controller) // Best effort at root level

			// Then enable in cagectl's subtree_control
			if err := writeFile(subtreeControl, "+"+controller); err != nil {
				// Log but don't fail — some controllers might not be delegated to us
				fmt.Fprintf(os.Stderr, "warning: could not enable %s controller: %v\n", controller, err)
			}
		}
	}

	// Create the container-specific cgroup
	if err := os.MkdirAll(m.Path, 0755); err != nil {
		return fmt.Errorf("failed to create container cgroup at %s: %w", m.Path, err)
	}

	return nil
}

// SetMemoryLimit configures the maximum memory a container can use.
//
// memory.max: Hard limit in bytes. If the cgroup's memory usage reaches this
// limit and can't be reduced by reclaiming, the OOM killer is invoked.
//
// memory.swap.max: We set this to 0 to prevent the container from using swap,
// which gives more predictable performance characteristics.
func (m *Manager) SetMemoryLimit(limitBytes int64) error {
	// Set hard memory limit
	memMax := filepath.Join(m.Path, "memory.max")
	if err := writeFile(memMax, strconv.FormatInt(limitBytes, 10)); err != nil {
		return fmt.Errorf("failed to set memory.max to %d: %w", limitBytes, err)
	}

	// Disable swap to prevent unpredictable performance
	swapMax := filepath.Join(m.Path, "memory.swap.max")
	if err := writeFile(swapMax, "0"); err != nil {
		// Swap controller might not be available, non-fatal
		fmt.Fprintf(os.Stderr, "warning: could not disable swap: %v\n", err)
	}

	return nil
}

// SetCPULimit configures CPU bandwidth limiting using the CFS bandwidth controller.
//
// cpu.max format: "$QUOTA $PERIOD" (both in microseconds)
//   - QUOTA: Maximum CPU time the cgroup can use per PERIOD
//   - PERIOD: The scheduling period (typically 100ms = 100000µs)
//
// Examples:
//   - "50000 100000" = 50% of one CPU core (50ms every 100ms)
//   - "100000 100000" = 100% of one CPU core
//   - "200000 100000" = 200% = 2 full CPU cores
//   - "max 100000" = unlimited (no CPU limit)
func (m *Manager) SetCPULimit(quota, period int64) error {
	cpuMax := filepath.Join(m.Path, "cpu.max")
	value := fmt.Sprintf("%d %d", quota, period)
	if err := writeFile(cpuMax, value); err != nil {
		return fmt.Errorf("failed to set cpu.max to %q: %w", value, err)
	}
	return nil
}

// SetPidsLimit configures the maximum number of processes in the cgroup.
//
// pids.max: Maximum number of tasks (threads + processes) that can exist.
// This is critical for preventing fork bombs — without it, a malicious or
// buggy process inside the container could create processes until the host
// runs out of PIDs, affecting all other containers and host processes.
func (m *Manager) SetPidsLimit(limit int64) error {
	pidsMax := filepath.Join(m.Path, "pids.max")
	if err := writeFile(pidsMax, strconv.FormatInt(limit, 10)); err != nil {
		return fmt.Errorf("failed to set pids.max to %d: %w", limit, err)
	}
	return nil
}

// AddProcess moves a process into this cgroup by writing its PID to cgroup.procs.
//
// Once a process is in a cgroup, all its resource usage is tracked and limited
// by that cgroup's settings. Child processes inherit the cgroup membership.
func (m *Manager) AddProcess(pid int) error {
	procsFile := filepath.Join(m.Path, "cgroup.procs")
	if err := writeFile(procsFile, strconv.Itoa(pid)); err != nil {
		return fmt.Errorf("failed to add PID %d to cgroup: %w", pid, err)
	}
	return nil
}

// GetStats reads current resource usage statistics from the cgroup.
func (m *Manager) GetStats() (*Stats, error) {
	stats := &Stats{}

	// Read memory usage
	memCurrent := filepath.Join(m.Path, "memory.current")
	if data, err := os.ReadFile(memCurrent); err == nil {
		val, _ := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
		stats.MemoryUsageBytes = val
	}

	// Read memory limit
	memMax := filepath.Join(m.Path, "memory.max")
	if data, err := os.ReadFile(memMax); err == nil {
		text := strings.TrimSpace(string(data))
		if text != "max" {
			val, _ := strconv.ParseInt(text, 10, 64)
			stats.MemoryLimitBytes = val
		}
	}

	// Read CPU stats
	cpuStat := filepath.Join(m.Path, "cpu.stat")
	if data, err := os.ReadFile(cpuStat); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			parts := strings.Fields(line)
			if len(parts) == 2 && parts[0] == "usage_usec" {
				val, _ := strconv.ParseInt(parts[1], 10, 64)
				stats.CPUUsageMicroseconds = val
			}
		}
	}

	// Read PID count
	pidsCurrent := filepath.Join(m.Path, "pids.current")
	if data, err := os.ReadFile(pidsCurrent); err == nil {
		val, _ := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
		stats.PidsCount = val
	}

	return stats, nil
}

// Cleanup removes the container's cgroup.
// All processes must be terminated before this is called.
func (m *Manager) Cleanup() error {
	// cgroup directories can only be removed when they have no processes.
	// os.Remove (not RemoveAll) is correct here — the kernel manages the
	// virtual files inside the cgroup directory.
	if err := os.Remove(m.Path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to remove cgroup %s: %w", m.Path, err)
	}
	return nil
}

// Stats holds resource usage data from the cgroup.
type Stats struct {
	MemoryUsageBytes     int64 `json:"memory_usage_bytes"`
	MemoryLimitBytes     int64 `json:"memory_limit_bytes"`
	CPUUsageMicroseconds int64 `json:"cpu_usage_microseconds"`
	PidsCount            int64 `json:"pids_count"`
}

// getAvailableControllers reads which controllers are available in the root cgroup.
func (m *Manager) getAvailableControllers() ([]string, error) {
	path := filepath.Join(CgroupV2Root, "cgroup.controllers")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(data)), nil
}

// writeFile is a helper that writes a string to a file.
// Cgroup control files are written to like regular files but have special semantics.
func writeFile(path, value string) error {
	return os.WriteFile(path, []byte(value), 0644)
}

// contains checks if a string slice contains a given string.
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
