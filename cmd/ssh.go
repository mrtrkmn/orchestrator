package cmd

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"

	"github.com/mrtrkmn/orchestrator/config"
	"github.com/mrtrkmn/orchestrator/orch"
	"github.com/mrtrkmn/orchestrator/orch/libvirtctl"
	"github.com/spf13/cobra"
)

var (
	sshUser    string
	sshKeyPath string
)

func init() {
	sshCmd.Flags().StringVarP(&sshUser, "user", "u", "debian", "SSH username")
	sshCmd.Flags().StringVarP(&sshKeyPath, "key", "i", "", "path to SSH private key")
	RootCmd.AddCommand(sshCmd)
}

var sshCmd = &cobra.Command{
	Use:   "ssh [vm-name]",
	Short: "SSH into a running VM",
	Long: `Open an interactive SSH session to a running VM.

The command discovers the VM's IP address automatically and connects via SSH.

If no VM name is provided, lists available VMs and their IPs.

Examples:
  orchestrator ssh demo-vm              # SSH as debian@<auto-detected-ip>
  orchestrator ssh demo-vm -u root      # SSH as root
  orchestrator ssh demo-vm -i ~/.ssh/id # SSH with a specific key`,
	Run: func(cmd *cobra.Command, args []string) {
		if configFile == "" {
			configFile = "config.yaml"
		}
		cfg, err := config.LoadConfig(configFile)
		if err != nil {
			cfg = nil // allow listing VMs without a config
		}

		var o *orch.Orchestrator
		if cfg != nil {
			o, err = orch.NewOrchestrator(cfg)
			if err != nil {
				log.Fatalf("creating orchestrator: %v", err)
			}
		}

		if len(args) == 0 {
			listVMsWithIP(cfg, o)
			return
		}

		vmName := args[0]
		vc := resolveVMClient(cfg, o)

		if !vc.VMExists(vmName) {
			log.Fatalf("VM '%s' is not defined", vmName)
		}

		state, err := vc.GetVMState(vmName)
		if err != nil {
			log.Fatalf("could not get VM state: %v", err)
		}
		if state != "running" {
			log.Fatalf("VM '%s' is %s (must be running)", vmName, state)
		}

		ip, err := discoverIP(vc, vmName)
		if err != nil {
			log.Fatalf("could not discover IP for '%s': %v\nTry: virsh domifaddr %s --source lease", vmName, err, vmName)
		}

		fmt.Printf("Connecting to %s@%s ...\n", sshUser, ip)

		sshArgs := []string{
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "LogLevel=ERROR",
		}
		if sshKeyPath != "" {
			sshArgs = append(sshArgs, "-i", sshKeyPath)
		}
		sshArgs = append(sshArgs, fmt.Sprintf("%s@%s", sshUser, ip))

		sshBin, err := exec.LookPath("ssh")
		if err != nil {
			log.Fatal("ssh binary not found in PATH")
		}

		proc := exec.Command(sshBin, sshArgs...)
		proc.Stdin = os.Stdin
		proc.Stdout = os.Stdout
		proc.Stderr = os.Stderr

		if err := proc.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				os.Exit(exitErr.ExitCode())
			}
			log.Fatalf("ssh failed: %v", err)
		}
	},
}

// resolveVMClient returns a libvirt client, preferring the one inside the
// orchestrator when available.
func resolveVMClient(cfg *config.Config, o *orch.Orchestrator) *libvirtctl.Client {
	if o != nil {
		return o.GetVMClient()
	}
	return libvirtctl.NewClient()
}

// discoverIP tries up to 30 s to resolve a VM's IP address.
func discoverIP(vc *libvirtctl.Client, vmName string) (string, error) {
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ip, err := vc.GetVMIP(vmName)
		if err == nil && ip != "" {
			return ip, nil
		}
		lastErr = err
		time.Sleep(2 * time.Second)
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("timeout waiting for IP address")
}

// listVMsWithIP prints all configured (or all libvirt) VMs with their IPs.
func listVMsWithIP(cfg *config.Config, o *orch.Orchestrator) {
	vc := resolveVMClient(cfg, o)

	if cfg != nil && len(cfg.VMs) > 0 {
		fmt.Println("VMs (from config):")
		for _, vm := range cfg.VMs {
			printVM(vc, vm.Name)
		}
	} else {
		vms, err := vc.ListVMs()
		if err != nil {
			log.Fatalf("listing VMs: %v", err)
		}
		fmt.Println("VMs (from libvirt):")
		for _, name := range vms {
			printVM(vc, name)
		}
	}
	fmt.Println("\nUsage: orchestrator ssh <vm-name> [-u user]")
}

func printVM(vc *libvirtctl.Client, name string) {
	state := "not defined"
	ip := ""
	if vc.VMExists(name) {
		if s, err := vc.GetVMState(name); err == nil {
			state = s
		}
		if state == "running" {
			if v, err := vc.GetVMIP(name); err == nil {
				ip = v
			}
		}
	}
	if ip != "" {
		fmt.Printf("  %-20s %s  (IP: %s)\n", name, state, ip)
	} else {
		fmt.Printf("  %-20s %s\n", name, state)
	}
}
