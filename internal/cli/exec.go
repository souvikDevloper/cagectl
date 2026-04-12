package cli

import (
	"fmt"

	"github.com/souvikinator/cagectl/internal/container"
	"github.com/spf13/cobra"
)

func newExecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec CONTAINER -- COMMAND [ARG...]",
		Short: "Run a command inside a running container",
		Long: `Execute a command inside an existing running container's namespaces.

This uses nsenter to join the container's PID, mount, UTS, IPC, and network
namespaces, then runs the specified command. This is equivalent to what
Docker does with 'docker exec'.`,

		Example: `  # Run a shell inside a container
  sudo cagectl exec my-container -- /bin/sh

  # Run a command and exit
  sudo cagectl exec abc123 -- ls -la /

  # Check container's network config
  sudo cagectl exec my-container -- ip addr show`,

		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireRoot(); err != nil {
				return err
			}

			containerRef := args[0]
			var command []string

			// Find the "--" separator
			for i, arg := range args {
				if arg == "--" && i+1 < len(args) {
					command = args[i+1:]
					break
				}
			}

			if len(command) == 0 {
				// If no --, treat remaining args as command
				if len(args) > 1 {
					command = args[1:]
				} else {
					return fmt.Errorf("no command specified — use: cagectl exec <container> -- <command>")
				}
			}

			// Find the container
			state, err := container.FindByIDOrName(containerRef)
			if err != nil {
				return err
			}

			// Execute the command
			rt := container.NewRuntime()
			return rt.Exec(state, command)
		},
	}

	return cmd
}
