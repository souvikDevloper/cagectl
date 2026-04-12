package cli

import (
	"fmt"

	"github.com/souvikDevloper/cagectl/internal/container"
	"github.com/spf13/cobra"
)

func newRemoveCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:     "remove CONTAINER [CONTAINER...]",
		Aliases: []string{"rm"},
		Short:   "Remove one or more containers",
		Long: `Remove containers and clean up all associated resources including cgroup,
overlay filesystem, and network interfaces. The container must be stopped
unless --force is used.`,

		Example: `  # Remove a stopped container
  sudo cagectl remove my-container

  # Force remove a running container
  sudo cagectl remove --force my-container

  # Remove multiple containers
  sudo cagectl rm container1 container2`,

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

				// Check if running and force flag
				if state.Status == container.StateRunning && !force {
					fmt.Fprintf(cmd.ErrOrStderr(),
						"Error: container %s is running. Use --force to remove it.\n", ref)
					lastErr = fmt.Errorf("container is running")
					continue
				}

				if err := rt.Remove(state); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "Error removing %s: %v\n", ref, err)
					lastErr = err
					continue
				}

				fmt.Printf("Removed %s (%s)\n", state.Name, shortID(state.ID))
			}

			return lastErr
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force remove running containers")

	return cmd
}
