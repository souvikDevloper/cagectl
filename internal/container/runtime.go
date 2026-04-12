// runtime.go contains the container lifecycle orchestration.
// It coordinates between namespaces, cgroups, filesystem, and networking
// to create, start, and stop containers.
package container

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/souvikDevloper/cagectl/internal/cgroup"
	"github.com/souvikDevloper/cagectl/internal/filesystem"
	"github.com/souvikDevloper/cagectl/internal/namespace"
	"github.com/souvikDevloper/cagectl/internal/network"
)

// Runtime orchestrates container lifecycle operations.
type Runtime struct{}

// NewRuntime creates a new container runtime instance.
func NewRuntime() *Runtime {
	return &Runtime{}
}

// Create sets up a new container but does not start it.
// Returns the container state with all paths and IDs populated.
func (r *Runtime) Create(cfg Config) (*ContainerState, error) {
	// Generate unique ID if not set
	if cfg.ID == "" {
		cfg.ID = uuid.New().String()
	}

	// Generate name if not set
	if cfg.Name == "" {
		cfg.Name = generateName()
	}

	// Validate the configuration
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Ensure state directories exist
	if err := EnsureDirectories(); err != nil {
		return nil, fmt.Errorf("failed to create directories: %w", err)
	}

	// Set up overlay filesystem
	fsMgr := filesystem.NewManager(cfg.ID, cfg.Filesystem.RootfsPath)
	if cfg.Filesystem.EnableOverlay {
		if err := fsMgr.Setup(); err != nil {
			return nil, fmt.Errorf("overlay setup failed: %w", err)
		}
	}

	// Copy DNS config into container rootfs
	mergedPath := fsMgr.GetMergedPath()
	if err := filesystem.CopyResolveConf(mergedPath); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not copy resolv.conf: %v\n", err)
	}

	// Set up cgroup
	cgMgr := cgroup.NewManager(cfg.ID)
	if err := cgMgr.Setup(); err != nil {
		return nil, fmt.Errorf("cgroup setup failed: %w", err)
	}

	// Configure resource limits
	if err := cgMgr.SetMemoryLimit(cfg.Resources.MemoryLimitBytes); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not set memory limit: %v\n", err)
	}
	if err := cgMgr.SetCPULimit(cfg.Resources.CPUQuota, cfg.Resources.CPUPeriod); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not set CPU limit: %v\n", err)
	}
	if err := cgMgr.SetPidsLimit(cfg.Resources.PidsLimit); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not set pids limit: %v\n", err)
	}

	now := time.Now()
	state := &ContainerState{
		ID:               cfg.ID,
		Name:             cfg.Name,
		Status:           StateCreated,
		Config:           cfg,
		CreatedAt:        now,
		CgroupPath:       cgMgr.Path,
		OverlayMountPath: fsMgr.GetMergedPath(),
		OverlayUpperDir:  fsMgr.Dirs.UpperDir,
		OverlayWorkDir:   fsMgr.Dirs.WorkDir,
	}

	// Persist state
	if err := SaveState(state); err != nil {
		return nil, fmt.Errorf("failed to save state: %w", err)
	}

	return state, nil
}

// Start launches the container process in isolated namespaces.
func (r *Runtime) Start(state *ContainerState) error {
	cfg := state.Config

	// Build the command arguments for the init process.
	// We pass configuration via environment variables and arguments
	// to the re-exec'd init process.
	initArgs := append([]string{
		"--rootfs", state.OverlayMountPath,
		"--hostname", cfg.Hostname,
		"--id", cfg.ID,
		"--",
	}, cfg.Command...)

	// Create the namespaced command
	cmd := namespace.SetupContainerCmd(initArgs)

	// Set environment variables for the container
	cmd.Env = cfg.Env

	// Start the process in new namespaces
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start container process: %w", err)
	}

	pid := cmd.Process.Pid

	// Add the process to the cgroup
	cgMgr := cgroup.NewManager(cfg.ID)
	if err := cgMgr.AddProcess(pid); err != nil {
		// Kill the process if we can't add it to the cgroup
		_ = cmd.Process.Kill()
		return fmt.Errorf("failed to add process to cgroup: %w", err)
	}

	// Set up networking if enabled
	if cfg.Network.EnableNetworking {
		if err := r.setupNetworking(cfg, pid); err != nil {
			fmt.Fprintf(os.Stderr, "warning: network setup failed: %v\n", err)
			// Don't kill the container — it can still work without networking
		}
	}

	// Update state
	now := time.Now()
	state.PID = pid
	state.Status = StateRunning
	state.StartedAt = &now

	if err := SaveState(state); err != nil {
		return fmt.Errorf("failed to update state: %w", err)
	}

	// Wait for the process to finish
	err := cmd.Wait()

	// Update state to stopped
	finishedAt := time.Now()
	state.Status = StateStopped
	state.FinishedAt = &finishedAt

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code := exitErr.ExitCode()
			state.ExitCode = &code
		}
	} else {
		code := 0
		state.ExitCode = &code
	}

	_ = SaveState(state)
	return err
}

// Stop sends a signal to terminate the container's init process.
func (r *Runtime) Stop(state *ContainerState) error {
	if state.Status != StateRunning {
		return fmt.Errorf("container %s is not running (status: %s)", state.ID, state.Status)
	}

	if !IsRunning(state.PID) {
		state.Status = StateStopped
		_ = SaveState(state)
		return nil
	}

	// Send SIGTERM first (graceful shutdown)
	process, err := os.FindProcess(state.PID)
	if err != nil {
		return fmt.Errorf("failed to find process %d: %w", state.PID, err)
	}

	if err := process.Signal(syscall.SIGTERM); err != nil {
		// Process might already be gone
		state.Status = StateStopped
		_ = SaveState(state)
		return nil
	}

	// Give it 10 seconds to gracefully shut down
	timeout := time.After(10 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			// Force kill with SIGKILL
			_ = process.Signal(syscall.SIGKILL)
			state.Status = StateStopped
			now := time.Now()
			state.FinishedAt = &now
			_ = SaveState(state)
			return nil
		case <-ticker.C:
			if !IsRunning(state.PID) {
				state.Status = StateStopped
				now := time.Now()
				state.FinishedAt = &now
				_ = SaveState(state)
				return nil
			}
		}
	}
}

// Remove cleans up all resources associated with a container.
func (r *Runtime) Remove(state *ContainerState) error {
	// Stop if running
	if state.Status == StateRunning && IsRunning(state.PID) {
		if err := r.Stop(state); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to stop container: %v\n", err)
		}
	}

	// Clean up cgroup
	cgMgr := cgroup.NewManager(state.ID)
	if err := cgMgr.Cleanup(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: cgroup cleanup failed: %v\n", err)
	}

	// Clean up overlay filesystem
	fsMgr := filesystem.NewManager(state.ID, state.Config.Filesystem.RootfsPath)
	if err := fsMgr.Cleanup(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: overlay cleanup failed: %v\n", err)
	}

	// Clean up networking
	if state.Config.Network.EnableNetworking {
		netMgr := network.NewManager(
			state.ID,
			state.Config.Network.BridgeName,
			state.Config.Network.ContainerIP,
			state.Config.Network.GatewayIP,
			state.Config.Network.Subnet,
			state.Config.Network.VethContainerName,
		)
		if err := netMgr.Cleanup(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: network cleanup failed: %v\n", err)
		}
	}

	// Remove persisted state
	return RemoveState(state.ID)
}

// Exec runs a command inside an existing running container's namespaces.
func (r *Runtime) Exec(state *ContainerState, command []string) error {
	if state.Status != StateRunning || !IsRunning(state.PID) {
		return fmt.Errorf("container %s is not running", state.ID)
	}

	// nsenter joins the existing namespaces of the container's init process.
	// This is the same approach Docker uses for `docker exec`.
	args := []string{
		fmt.Sprintf("--target=%d", state.PID),
		"--mount", "--uts", "--ipc", "--net", "--pid",
		"--",
	}
	args = append(args, command...)

	cmd := exec.Command("nsenter", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// setupNetworking configures the container's network.
func (r *Runtime) setupNetworking(cfg Config, containerPID int) error {
	// Allocate an IP for this container
	containerIP := cfg.Network.ContainerIP
	if containerIP == "" {
		var err error
		containerIP, err = network.AllocateIP(cfg.Network.Subnet, cfg.Network.GatewayIP)
		if err != nil {
			return fmt.Errorf("IP allocation failed: %w", err)
		}
	}

	netMgr := network.NewManager(
		cfg.ID,
		cfg.Network.BridgeName,
		containerIP,
		cfg.Network.GatewayIP,
		cfg.Network.Subnet,
		cfg.Network.VethContainerName,
	)

	// Create bridge (idempotent)
	if err := netMgr.SetupBridge(); err != nil {
		return fmt.Errorf("bridge setup failed: %w", err)
	}

	// Enable IP forwarding
	if err := network.EnableIPForwarding(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not enable IP forwarding: %v\n", err)
	}

	// Set up NAT
	if err := network.SetupNAT(cfg.Network.Subnet, cfg.Network.BridgeName); err != nil {
		fmt.Fprintf(os.Stderr, "warning: NAT setup failed: %v\n", err)
	}

	// Create veth pair and attach to bridge
	if err := netMgr.SetupVethPair(containerPID); err != nil {
		return fmt.Errorf("veth setup failed: %w", err)
	}

	// Configure networking inside the container namespace
	if err := netMgr.ConfigureContainerNetwork(containerPID); err != nil {
		return fmt.Errorf("container network config failed: %w", err)
	}

	return nil
}

// generateName creates a random container name (adjective-noun format).
func generateName() string {
	adjectives := []string{"brave", "calm", "eager", "fierce", "gentle", "happy",
		"jolly", "keen", "lively", "merry", "noble", "proud", "quick", "sharp",
		"swift", "vivid", "wise", "bold", "cool", "deft"}
	nouns := []string{"falcon", "tiger", "wolf", "bear", "eagle", "hawk",
		"lion", "lynx", "raven", "shark", "viper", "cobra", "crane", "fox",
		"orca", "puma", "stag", "wren", "yak", "elk"}

	// Use current time nanoseconds for simple randomness
	now := time.Now().UnixNano()
	adj := adjectives[now%int64(len(adjectives))]
	noun := nouns[(now/int64(len(adjectives)))%int64(len(nouns))]
	return fmt.Sprintf("%s-%s", adj, noun)
}
