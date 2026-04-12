// Package cli implements the command-line interface for cagectl.
//
// Commands:
//   - cagectl run     — Create and start a container
//   - cagectl exec    — Run a command in a running container
//   - cagectl list    — List all containers
//   - cagectl stop    — Stop a running container
//   - cagectl remove  — Remove a container and clean up resources
//   - cagectl inspect — Show detailed container information
//   - cagectl logs    — View resource usage stats
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Version info — injected at build time via ldflags
var (
	Version   = "dev"
	BuildDate = "unknown"
	GitCommit = "unknown"
)

// NewRootCmd creates the root cobra command for cagectl.
func NewRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "cagectl",
		Short: "A lightweight container runtime built from scratch",
		Long: `cagectl is a minimal container runtime that demonstrates how Linux containers
work under the hood. It uses Linux namespaces for isolation, cgroups v2 for
resource limits, OverlayFS for layered filesystems, and veth pairs for networking.

Built as a deep-dive into container internals — the same primitives that
Docker, containerd, and Kubernetes use at their core.

Examples:
  # Run an interactive shell in a container
  sudo cagectl run --rootfs /path/to/alpine-rootfs -- /bin/sh

  # Run with resource limits
  sudo cagectl run --rootfs ./rootfs --memory 128m --cpus 0.5 -- /bin/sh

  # List running containers
  sudo cagectl list

  # Execute a command in a running container
  sudo cagectl exec <container-id> -- ls -la /

  # Stop and remove a container
  sudo cagectl stop <container-id>
  sudo cagectl remove <container-id>`,

		Version: fmt.Sprintf("%s (commit: %s, built: %s)", Version, GitCommit, BuildDate),

		// Silence usage on errors — we'll print our own error messages
		SilenceUsage: true,
	}

	// Add subcommands
	rootCmd.AddCommand(
		newRunCmd(),
		newExecCmd(),
		newListCmd(),
		newStopCmd(),
		newRemoveCmd(),
		newInspectCmd(),
		newInitCmd(),
	)

	return rootCmd
}

// Execute runs the root command. This is the main entry point.
func Execute() {
	rootCmd := NewRootCmd()
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// requireRoot checks that cagectl is running as root.
// Container operations require root for namespace creation, cgroup management,
// mount operations, and network configuration.
func requireRoot() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("cagectl must be run as root (use sudo)")
	}
	return nil
}
