package cmd

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/mrtrkmn/orchestrator/config"
	"github.com/mrtrkmn/orchestrator/orch"
	"github.com/spf13/cobra"
)

func init() {
	RootCmd.AddCommand(statusCmd)
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show status of orchestrated resources",
	Run: func(cmd *cobra.Command, args []string) {
		if configFile == "" {
			configFile = "config.yaml"
		}
		cfg, err := config.LoadConfig(configFile)
		if err != nil {
			log.Fatalf("load config: %v", err)
		}
		o, err := orch.NewOrchestrator(cfg)
		if err != nil {
			log.Fatalf("creating orchestrator: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := o.Status(ctx)
		if err != nil {
			log.Fatalf("status: %v", err)
		}
		fmt.Print(out)
	},
}
