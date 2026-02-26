package cmd

import (
	"fmt"
	"log"
	"os"
	"os/exec"

	"github.com/mrtrkmn/orchestrator/config"
	"github.com/mrtrkmn/orchestrator/orch"
	"github.com/spf13/cobra"
)

func init() {
	RootCmd.AddCommand(consoleCmd)
}

var consoleCmd = &cobra.Command{
	Use:   "console [vm-name]",
	Short: "Connect to VM console or list VMs",
	Long: `Connect to a VM's serial console for direct access.
If no VM name is provided, lists all available VMs.

Usage:
  orchestrator console           # List all VMs
  orchestrator console demo-vm   # Connect to demo-vm console
  
To exit console: Press Ctrl + ]`,
	Run: func(cmd *cobra.Command, args []string) {
		// If no VM name provided, try to list VMs from config
		if len(args) == 0 {
			if configFile == "" {
				configFile = "config.yaml"
			}
			cfg, err := config.LoadConfig(configFile)
			if err != nil {
				// If no config, list all libvirt VMs
				fmt.Println("Available VMs (from libvirt):")
				cmd := exec.Command("virsh", "list", "--all", "--name")
				output, _ := cmd.Output()
				fmt.Print(string(output))
				fmt.Println("\nUsage: orchestrator console <vm-name>")
				return
			}

			o, err := orch.NewOrchestrator(cfg)
			if err != nil {
				log.Fatalf("creating orchestrator: %v", err)
			}

			fmt.Println("Available VMs:")
			for _, vmCfg := range cfg.VMs {
				state := "not defined"
				if o.GetVMClient().VMExists(vmCfg.Name) {
					if s, err := o.GetVMClient().GetVMState(vmCfg.Name); err == nil {
						state = s
					}
				}
				fmt.Printf("  %s: %s\n", vmCfg.Name, state)
			}
			fmt.Println("\nUsage: orchestrator console <vm-name>")
			return
		}

		vmName := args[0]

		// Connect directly without requiring config
		// Check if VM exists in libvirt
		checkCmd := exec.Command("virsh", "dominfo", vmName)
		if err := checkCmd.Run(); err != nil {
			log.Fatalf("VM '%s' not found", vmName)
		}

		// Connect to console
		fmt.Printf("Connecting to console of '%s'...\n", vmName)
		fmt.Println("To exit: Press Ctrl + ]")
		fmt.Println()

		consoleCmd := exec.Command("virsh", "console", vmName)
		consoleCmd.Stdin = os.Stdin
		consoleCmd.Stdout = os.Stdout
		consoleCmd.Stderr = os.Stderr

		if err := consoleCmd.Run(); err != nil {
			log.Fatalf("console connection failed: %v", err)
		}
	},
}
