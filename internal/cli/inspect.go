package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/souvikDevloper/cagectl/internal/cgroup"
	"github.com/souvikDevloper/cagectl/internal/container"
	"github.com/spf13/cobra"
)

func newInspectCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "inspect CONTAINER",
		Short: "Show detailed container information",
		Long: `Display detailed information about a container, including its configuration,
state, resource usage, and filesystem details.`,

		Example: `  # Inspect a container
  sudo cagectl inspect my-container

  # JSON output for scripting
  sudo cagectl inspect --json my-container`,

		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireRoot(); err != nil {
				return err
			}

			state, err := container.FindByIDOrName(args[0])
			if err != nil {
				return err
			}

			// Refresh status
			if state.Status == container.StateRunning && !container.IsRunning(state.PID) {
				state.Status = container.StateStopped
				_ = container.SaveState(state)
			}

			if jsonOutput {
				data, err := json.MarshalIndent(state, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}

			// Pretty-print the state
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

			fmt.Fprintf(w, "ID:\t%s\n", state.ID)
			fmt.Fprintf(w, "Name:\t%s\n", state.Name)
			fmt.Fprintf(w, "Status:\t%s\n", colorStatus(state.Status))
			fmt.Fprintf(w, "PID:\t%d\n", state.PID)
			fmt.Fprintf(w, "Created:\t%s\n", state.CreatedAt.Format("2006-01-02 15:04:05"))

			if state.StartedAt != nil {
				fmt.Fprintf(w, "Started:\t%s\n", state.StartedAt.Format("2006-01-02 15:04:05"))
			}
			if state.FinishedAt != nil {
				fmt.Fprintf(w, "Finished:\t%s\n", state.FinishedAt.Format("2006-01-02 15:04:05"))
			}
			if state.ExitCode != nil {
				fmt.Fprintf(w, "Exit Code:\t%d\n", *state.ExitCode)
			}

			fmt.Fprintln(w, "\n--- Configuration ---")
			fmt.Fprintf(w, "Command:\t%v\n", state.Config.Command)
			fmt.Fprintf(w, "Hostname:\t%s\n", state.Config.Hostname)
			fmt.Fprintf(w, "Rootfs:\t%s\n", state.Config.Filesystem.RootfsPath)

			fmt.Fprintln(w, "\n--- Resource Limits ---")
			fmt.Fprintf(w, "Memory:\t%s\n", formatBytes(state.Config.Resources.MemoryLimitBytes))
			fmt.Fprintf(w, "CPU:\t%.2f cores\n",
				float64(state.Config.Resources.CPUQuota)/float64(state.Config.Resources.CPUPeriod))
			fmt.Fprintf(w, "PIDs:\t%d\n", state.Config.Resources.PidsLimit)

			fmt.Fprintln(w, "\n--- Network ---")
			fmt.Fprintf(w, "Networking:\t%v\n", state.Config.Network.EnableNetworking)
			if state.Config.Network.EnableNetworking {
				fmt.Fprintf(w, "Bridge:\t%s\n", state.Config.Network.BridgeName)
				fmt.Fprintf(w, "Container IP:\t%s\n", state.Config.Network.ContainerIP)
				fmt.Fprintf(w, "Gateway:\t%s\n", state.Config.Network.GatewayIP)
			}

			fmt.Fprintln(w, "\n--- Filesystem ---")
			fmt.Fprintf(w, "Overlay Mount:\t%s\n", state.OverlayMountPath)
			fmt.Fprintf(w, "Upper Layer:\t%s\n", state.OverlayUpperDir)
			fmt.Fprintf(w, "Cgroup Path:\t%s\n", state.CgroupPath)

			// Show live resource usage if running
			if state.Status == container.StateRunning {
				cgMgr := cgroup.NewManager(state.ID)
				stats, err := cgMgr.GetStats()
				if err == nil {
					fmt.Fprintln(w, "\n--- Live Resource Usage ---")
					fmt.Fprintf(w, "Memory Used:\t%s / %s\n",
						formatBytes(stats.MemoryUsageBytes),
						formatBytes(stats.MemoryLimitBytes))
					fmt.Fprintf(w, "CPU Time:\t%dms\n", stats.CPUUsageMicroseconds/1000)
					fmt.Fprintf(w, "Processes:\t%d\n", stats.PidsCount)
				}
			}

			if err := w.Flush(); err != nil {
				return err
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	return cmd
}

// formatBytes converts bytes to human-readable format.
func formatBytes(b int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)

	switch {
	case b >= GB:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
