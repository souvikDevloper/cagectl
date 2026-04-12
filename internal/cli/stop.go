package cli

import (
	"fmt"

	"github.com/souvikinator/cagectl/internal/container"
	"github.com/spf13/cobra"
)

func newStopCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop CONTAINER [CONTAINER...]",
		Short: "Stop one or more running containers",
		Long: `Gracefully stop running containers. Sends SIGTERM first and waits up to 10
seconds for the container to exit. If the container doesn't stop gracefully,
it is forcefully killed with SIGKILL.`,

		Example: `  # Stop a container by name
  sudo cagectl stop my-container

  # Stop by ID (short or full)
  sudo cagectl stop abc123

  # Stop multiple containers
  sudo cagectl stop container1 container2`,

		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireRoot(); err != nil {
				return err
			}

			rt := container.NewRuntime()
			var lastErr error

			for _, ref := range args {
				state, err := container.FindByIDOrName(ref)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "Error: %v\n", err)
					lastErr = err
					continue
				}

				if err := rt.Stop(state); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "Error stopping %s: %v\n", ref, err)
					lastErr = err
					continue
				}

				fmt.Printf("Stopped %s (%s)\n", state.Name, shortID(state.ID))
			}

			return lastErr
		},
	}

	return cmd
}
