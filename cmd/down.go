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
	RootCmd.AddCommand(downCmd)
}

var downCmd = &cobra.Command{
	Use:   "down",
	Short: "Stop and remove orchestrated resources",
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
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := o.Down(ctx); err != nil {
			log.Fatalf("down: %v", err)
		}
		fmt.Println("Down completed")
	},
}
