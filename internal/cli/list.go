package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/souvikDevloper/cagectl/internal/container"
	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	var showAll bool

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls", "ps"},
		Short:   "List containers",
		Long: `List all containers and their status. By default, only shows running containers.
Use --all to include stopped containers.`,

		Example: `  # List running containers
  sudo cagectl list

  # List all containers (including stopped)
  sudo cagectl list --all`,

		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireRoot(); err != nil {
				return err
			}

			states, err := container.ListStates()
			if err != nil {
				return fmt.Errorf("failed to list containers: %w", err)
			}

			if len(states) == 0 {
				fmt.Println("No containers found.")
				return nil
			}

			// Tab-aligned table output
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "CONTAINER ID\tNAME\tSTATUS\tPID\tCREATED\tCOMMAND"); err != nil {
				return err
			}

			for _, state := range states {
				// Refresh running status
				if state.Status == container.StateRunning && !container.IsRunning(state.PID) {
					state.Status = container.StateStopped
					_ = container.SaveState(state)
				}

				// Filter by status
				if !showAll && state.Status != container.StateRunning {
					continue
				}

				// Format the command (truncate if too long)
				cmdStr := strings.Join(state.Config.Command, " ")
				if len(cmdStr) > 30 {
					cmdStr = cmdStr[:27] + "..."
				}

				// Format the created time
				created := formatTimeAgo(state.CreatedAt)

				// Format PID
				pidStr := "-"
				if state.PID > 0 && state.Status == container.StateRunning {
					pidStr = fmt.Sprintf("%d", state.PID)
				}

				if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					id, name, status, pid, created, command); err != nil {
					return err
				}
			}

			if err := w.Flush(); err != nil {
				return err
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&showAll, "all", "a", false, "Show all containers (including stopped)")

	return cmd
}

// formatTimeAgo returns a human-friendly relative time string.
func formatTimeAgo(t time.Time) string {
	d := time.Since(t)

	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// colorStatus returns a styled status string.
func colorStatus(status container.State) string {
	switch status {
	case container.StateRunning:
		return "\033[32mrunning\033[0m" // green
	case container.StateStopped:
		return "\033[31mstopped\033[0m" // red
	case container.StateCreated:
		return "\033[33mcreated\033[0m" // yellow
	case container.StateCreating:
		return "\033[33mcreating\033[0m" // yellow
	default:
		return string(status)
	}
}
