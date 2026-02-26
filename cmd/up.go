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
	RootCmd.AddCommand(upCmd)
}

var upCmd = &cobra.Command{
	Use:   "up",
	Short: "Create network and start configured containers and VMs",
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

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		if err := o.Up(ctx); err != nil {
			log.Fatalf("orchestrator up failed: %v", err)
		}
		fmt.Println("Up completed")
	},
}
