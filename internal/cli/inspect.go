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
				if _, err := fmt.Fprintln(os.Stdout, string(data)); err != nil {
					return err
				}
				return nil
			}

			// Pretty-print the state
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

			writef := func(format string, args ...any) error {
				_, err := fmt.Fprintf(w, format, args...)
				return err
			}
			writeln := func(args ...any) error {
				_, err := fmt.Fprintln(w, args...)
				return err
			}

			if err := writef("ID:\t%s\n", state.ID); err != nil {
				return err
			}
			if err := writef("Name:\t%s\n", state.Name); err != nil {
				return err
			}
			if err := writef("Status:\t%s\n", colorStatus(state.Status)); err != nil {
				return err
			}
			if err := writef("PID:\t%d\n", state.PID); err != nil {
				return err
			}
			if err := writef("Created:\t%s\n", state.CreatedAt.Format("2006-01-02 15:04:05")); err != nil {
				return err
			}

			if state.StartedAt != nil {
				if err := writef("Started:\t%s\n", state.StartedAt.Format("2006-01-02 15:04:05")); err != nil {
					return err
				}
			}
			if state.FinishedAt != nil {
				if err := writef("Finished:\t%s\n", state.FinishedAt.Format("2006-01-02 15:04:05")); err != nil {
					return err
				}
			}
			if state.ExitCode != nil {
				if err := writef("Exit Code:\t%d\n", *state.ExitCode); err != nil {
					return err
				}
			}

			if err := writeln(); err != nil {
				return err
			}
			if err := writeln("--- Configuration ---"); err != nil {
				return err
			}
			if err := writef("Command:\t%v\n", state.Config.Command); err != nil {
				return err
			}
			if err := writef("Hostname:\t%s\n", state.Config.Hostname); err != nil {
				return err
			}
			if err := writef("Rootfs:\t%s\n", state.Config.Filesystem.RootfsPath); err != nil {
				return err
			}

			if err := writeln(); err != nil {
				return err
			}
			if err := writeln("--- Resource Limits ---"); err != nil {
				return err
			}
			if err := writef("Memory:\t%s\n", formatBytes(state.Config.Resources.MemoryLimitBytes)); err != nil {
				return err
			}
			if err := writef(
				"CPU:\t%.2f cores\n",
				float64(state.Config.Resources.CPUQuota)/float64(state.Config.Resources.CPUPeriod),
			); err != nil {
				return err
			}
			if err := writef("PIDs:\t%d\n", state.Config.Resources.PidsLimit); err != nil {
				return err
			}

			if err := writeln(); err != nil {
				return err
			}
			if err := writeln("--- Network ---"); err != nil {
				return err
			}
			if err := writef("Networking:\t%v\n", state.Config.Network.EnableNetworking); err != nil {
				return err
			}
			if state.Config.Network.EnableNetworking {
				if err := writef("Bridge:\t%s\n", state.Config.Network.BridgeName); err != nil {
					return err
				}
				if err := writef("Container IP:\t%s\n", state.Config.Network.ContainerIP); err != nil {
					return err
				}
				if err := writef("Gateway:\t%s\n", state.Config.Network.GatewayIP); err != nil {
					return err
				}
			}

			if err := writeln(); err != nil {
				return err
			}
			if err := writeln("--- Filesystem ---"); err != nil {
				return err
			}
			if err := writef("Overlay Mount:\t%s\n", state.OverlayMountPath); err != nil {
				return err
			}
			if err := writef("Upper Layer:\t%s\n", state.OverlayUpperDir); err != nil {
				return err
			}
			if err := writef("Cgroup Path:\t%s\n", state.CgroupPath); err != nil {
				return err
			}

			// Show live resource usage if running
			if state.Status == container.StateRunning {
				cgMgr := cgroup.NewManager(state.ID)
				stats, err := cgMgr.GetStats()
				if err == nil {
					if err := writeln(); err != nil {
						return err
					}
					if err := writeln("--- Live Resource Usage ---"); err != nil {
						return err
					}
					if err := writef(
						"Memory Used:\t%s / %s\n",
						formatBytes(stats.MemoryUsageBytes),
						formatBytes(stats.MemoryLimitBytes),
					); err != nil {
						return err
					}
					if err := writef("CPU Time:\t%dms\n", stats.CPUUsageMicroseconds/1000); err != nil {
						return err
					}
					if err := writef("Processes:\t%d\n", stats.PidsCount); err != nil {
						return err
					}
				}
			}

			if err := w.Flush(); err != nil {
				return err
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}
