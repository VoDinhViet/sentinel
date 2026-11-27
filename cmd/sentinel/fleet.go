package main

import (
	"time"

	"github.com/spf13/cobra"

	"sentinel/internal/fleet"
)

// newFleetCmd is `sentinel fleet`: how the other servers are doing, read from the HTTP API of
// their daemons. It never reads config.yaml, so a laptop with only a fleet file can run it.
func newFleetCmd() *cobra.Command {
	var fleetPath string
	fleetCmd := &cobra.Command{
		Use:   "fleet",
		Short: "Xem nhiều server Sentinel cùng lúc, qua HTTP API của daemon trên mỗi server",
	}
	fleetCmd.PersistentFlags().StringVar(&fleetPath, "fleet-config", "", "file fleet (mặc định: $SENTINEL_FLEET_CONFIG hoặc ~/.config/sentinel/fleet.yaml)")
	fleetCmd.AddCommand(newFleetStatusCmd(&fleetPath), newFleetAlertsCmd(&fleetPath))
	return fleetCmd
}

func newFleetStatusCmd(fleetPath *string) *cobra.Command {
	var output string
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Xem tình trạng của từng server: backup, uptime check và cảnh báo đang mở",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkOutput(output); err != nil {
				return err
			}
			answers, err := fetchFleet(cmd, *fleetPath)
			if err != nil {
				return err
			}
			if output == outputJSON {
				return writeJSON(cmd.OutOrStdout(), fleetStatusJSON(answers))
			}
			return printFleetStatus(cmd.OutOrStdout(), answers)
		},
	}
	addOutputFlag(statusCmd, &output)
	return statusCmd
}

func newFleetAlertsCmd(fleetPath *string) *cobra.Command {
	var output string
	alertsCmd := &cobra.Command{
		Use:   "alerts",
		Short: "Xem cảnh báo đang mở của mọi server, kể cả server không truy cập được",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkOutput(output); err != nil {
				return err
			}
			answers, err := fetchFleet(cmd, *fleetPath)
			if err != nil {
				return err
			}
			alerts := fleetAlerts(answers)
			if output == outputJSON {
				return writeJSON(cmd.OutOrStdout(), alerts)
			}
			return printFleetAlerts(cmd.OutOrStdout(), alerts, time.Now())
		},
	}
	addOutputFlag(alertsCmd, &output)
	return alertsCmd
}

// fetchFleet reads the fleet file and asks every server in it.
func fetchFleet(cmd *cobra.Command, fleetPath string) ([]fleet.ServerStatus, error) {
	if fleetPath == "" {
		fleetPath = fleet.DefaultPath()
	}
	cfg, err := fleet.Load(fleetPath)
	if err != nil {
		return nil, err
	}
	return fleet.Fetch(cmd.Context(), cfg.Servers, fleet.DefaultParallel), nil
}
