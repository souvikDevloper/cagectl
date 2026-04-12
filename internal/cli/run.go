package cli

import (
	"fmt"

	"github.com/souvikinator/cagectl/internal/container"
	"github.com/spf13/cobra"
)

func newRunCmd() *cobra.Command {
	var (
		rootfs   string
		name     string
		hostname string
		memory   string
		cpus     float64
		pids     int64
		noNet    bool
	)

	cmd := &cobra.Command{
		Use:   "run [flags] -- COMMAND [ARG...]",
		Short: "Create and start a new container",
		Long: `Create and run a new container with Linux namespace isolation, cgroup resource
limits, OverlayFS filesystem, and veth networking.

The container gets its own PID namespace (it sees itself as PID 1), mount
namespace (isolated filesystem via OverlayFS), UTS namespace (own hostname),
IPC namespace (isolated shared memory), and network namespace (own IP address
on a virtual bridge network).

Resource limits are enforced via cgroup v2 controllers.`,

		Example: `  # Interactive shell with Alpine rootfs
  sudo cagectl run --rootfs ./rootfs -- /bin/sh

  # Limited resources
  sudo cagectl run --rootfs ./rootfs --memory 64m --cpus 0.25 --pids 32 -- /bin/sh

  # Custom hostname, no networking
  sudo cagectl run --rootfs ./rootfs --hostname mybox --no-net -- /bin/sh

  # Run a specific command
  sudo cagectl run --rootfs ./rootfs -- /bin/echo "Hello from container!"`,

		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireRoot(); err != nil {
				return err
			}

			// Everything after "--" is the command
			command := args
			if len(command) == 0 {
				return fmt.Errorf("no command specified — use: cagectl run --rootfs <path> -- <command>")
			}

			// Parse memory string (e.g., "128m", "1g")
			memBytes, err := parseMemoryString(memory)
			if err != nil {
				return fmt.Errorf("invalid memory value %q: %w", memory, err)
			}

			// Build config
			cfg := container.NewDefaultConfig()
			cfg.Name = name
			cfg.Command = command
			cfg.Hostname = hostname
			cfg.Filesystem.RootfsPath = rootfs
			cfg.Network.EnableNetworking = !noNet

			if memBytes > 0 {
				cfg.Resources.MemoryLimitBytes = memBytes
			}
			if cpus > 0 {
				// Convert CPU fraction to quota/period.
				// e.g., 0.5 CPUs = 50000µs quota per 100000µs period
				cfg.Resources.CPUQuota = int64(cpus * float64(cfg.Resources.CPUPeriod))
			}
			if pids > 0 {
				cfg.Resources.PidsLimit = pids
			}

			// Create and start the container
			rt := container.NewRuntime()

			fmt.Printf("Creating container...\n")
			state, err := rt.Create(cfg)
			if err != nil {
				return fmt.Errorf("failed to create container: %w", err)
			}

			fmt.Printf("Container %s (%s) created\n", state.Name, shortID(state.ID))
			fmt.Printf("Starting container...\n")

			if err := rt.Start(state); err != nil {
				// Clean up on failure
				rt.Remove(state)
				return fmt.Errorf("failed to start container: %w", err)
			}

			fmt.Printf("\nContainer %s exited", state.Name)
			if state.ExitCode != nil {
				fmt.Printf(" (exit code: %d)", *state.ExitCode)
			}
			fmt.Println()

			return nil
		},
	}

	cmd.Flags().StringVar(&rootfs, "rootfs", "", "Path to the root filesystem (required)")
	cmd.Flags().StringVar(&name, "name", "", "Container name (auto-generated if empty)")
	cmd.Flags().StringVar(&hostname, "hostname", "cage", "Hostname inside the container")
	cmd.Flags().StringVar(&memory, "memory", "", "Memory limit (e.g., 128m, 1g)")
	cmd.Flags().Float64Var(&cpus, "cpus", 0, "CPU limit (e.g., 0.5 for half a core)")
	cmd.Flags().Int64Var(&pids, "pids", 0, "Maximum number of processes")
	cmd.Flags().BoolVar(&noNet, "no-net", false, "Disable networking")

	cmd.MarkFlagRequired("rootfs")

	return cmd
}

// parseMemoryString converts human-readable memory strings to bytes.
// Supports: 512k, 128m, 1g, or plain bytes.
func parseMemoryString(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}

	var multiplier int64 = 1
	numStr := s

	switch s[len(s)-1] {
	case 'k', 'K':
		multiplier = 1024
		numStr = s[:len(s)-1]
	case 'm', 'M':
		multiplier = 1024 * 1024
		numStr = s[:len(s)-1]
	case 'g', 'G':
		multiplier = 1024 * 1024 * 1024
		numStr = s[:len(s)-1]
	}

	var value int64
	_, err := fmt.Sscanf(numStr, "%d", &value)
	if err != nil {
		return 0, err
	}

	return value * multiplier, nil
}

// shortID returns the first 12 characters of a container ID (like Docker).
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
