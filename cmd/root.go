package cmd

import (
	"fmt"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var (
	configFile string
	logLevel   string
)

var RootCmd = &cobra.Command{
	Use:   "orchestrator",
	Short: "Minimal orchestrator for demoing Docker & libvirt/KVM management",
}

func init() {
	RootCmd.PersistentFlags().StringVarP(&configFile, "config", "c", "", "path to config file (default: config.yaml)")
	RootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info", "log level (trace|debug|info|warn|error|fatal|panic)")
	RootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		lvl, err := log.ParseLevel(logLevel)
		if err != nil {
			return fmt.Errorf("invalid log level %q: %w", logLevel, err)
		}
		log.SetLevel(lvl)
		log.SetFormatter(&log.TextFormatter{FullTimestamp: true})
		log.AddHook(&goroutineHook{})
		return nil
	}
}
