package cli

import (
	"os"

	"github.com/souvikinator/cagectl/internal/container"
	"github.com/spf13/cobra"
)

// newInitCmd creates the hidden "init" subcommand.
//
// This command is NOT meant to be called by users directly. It is invoked
// automatically when the container runtime re-execs itself (/proc/self/exe init ...)
// inside the new namespaces.
//
// The init process:
//  1. Receives the rootfs path, hostname, and command as arguments
//  2. Sets up mounts (proc, dev, sys) inside the mount namespace
//  3. Calls pivot_root to switch to the container's rootfs
//  4. Sets the hostname in the UTS namespace
//  5. exec()'s the user's command (becoming PID 1)
func newInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "init",
		Hidden: true, // Users should never call this directly
		RunE: func(cmd *cobra.Command, args []string) error {
			// This runs INSIDE the new namespaces.
			// Hand off to the container init logic.
			if err := container.RunInit(args); err != nil {
				// Write error to stderr and exit with error code.
				// We can't return errors normally because exec() replaces the process.
				os.Stderr.WriteString("container init error: " + err.Error() + "\n")
				os.Exit(1)
			}
			return nil
		},
	}

	// Disable flag parsing — everything is passed through to RunInit
	cmd.DisableFlagParsing = true

	return cmd
}
