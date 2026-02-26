package orch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mrtrkmn/orchestrator/config"
	"github.com/mrtrkmn/orchestrator/orch/dockerctl"
	"github.com/mrtrkmn/orchestrator/orch/imagebuilder"
	"github.com/mrtrkmn/orchestrator/orch/ipool"
	"github.com/mrtrkmn/orchestrator/orch/libvirtctl"
	"github.com/mrtrkmn/orchestrator/wg"
	log "github.com/sirupsen/logrus"
)

type Orchestrator struct {
	cfg  *config.Config
	dc   *dockerctl.Client
	vc   *libvirtctl.Client
	pool *ipool.IPPool
	log  *log.Entry
}

type state struct {
	Network        string `json:"network"`
	CreatedNetwork bool   `json:"created_network"`
	DHCPContainer  string `json:"dhcp_container"`
	DNSContainer   string `json:"dns_container"`
	TempDir        string `json:"temp_dir"`
	ContainerLabel string `json:"container_label"`
}

var stateFile = "orch-state.json"

func NewOrchestrator(cfg *config.Config) (*Orchestrator, error) {
	dc, err := dockerctl.NewClient()
	if err != nil {
		return nil, err
	}
	vc := libvirtctl.NewClient()

	// create IP pool from subnet
	pool, err := ipool.NewIPPool(cfg.Subnet)
	if err != nil {
		return nil, err
	}

	logger := log.WithField("component", "orchestrator")

	return &Orchestrator{cfg: cfg, dc: dc, vc: vc, pool: pool, log: logger}, nil
}

// GetVMClient returns the libvirt client for direct access
func (o *Orchestrator) GetVMClient() *libvirtctl.Client {
	return o.vc
}

func (o *Orchestrator) Up(ctx context.Context) error {
	startLog := o.log.WithFields(log.Fields{
		"network":    o.cfg.NetworkName,
		"subnet":     o.cfg.Subnet,
		"attach_to":  o.cfg.AttachTo,
		"containers": len(o.cfg.Containers),
		"vms":        len(o.cfg.VMs),
		"wireguard":  o.cfg.Wireguard.Enabled,
	})
	startLog.Info("starting orchestrator up sequence")

	netName := o.cfg.NetworkName
	createdNetwork := false
	if o.cfg.AttachTo != "" {
		attachLog := o.log.WithField("network", o.cfg.AttachTo)
		attachLog.Info("validating attach-to network")
		if _, err := o.dc.NetworkInfo(o.cfg.AttachTo); err != nil {
			attachLog.WithError(err).Error("attach-to network validation failed")
			return fmt.Errorf("attach-to network not found: %w", err)
		}
		netName = o.cfg.AttachTo
	} else {
		driver := "bridge"
		if o.cfg.NetworkType == config.NetworkMacVlan {
			driver = "macvlan"
		}
		netLog := o.log.WithFields(log.Fields{
			"network": o.cfg.NetworkName,
			"subnet":  o.cfg.Subnet,
			"driver":  driver,
		})
		netLog.Info("creating docker network")
		if err := o.dc.CreateNetwork(o.cfg.NetworkName, o.cfg.Subnet, driver); err != nil {
			if !strings.Contains(err.Error(), "already exists") {
				netLog.WithError(err).Error("failed to create network")
				return fmt.Errorf("create network: %w", err)
			}
			netLog.Info("network already exists, reusing")
		} else {
			netLog.Info("network created")
		}
		createdNetwork = true
	}

	servicesLog := o.log.WithField("network", netName)
	servicesLog.Info("ensuring DHCP and DNS services")
	dhcpName, dnsName, tempDir, err := o.dc.EnsureDHCPAndDNS(ctx, netName, o.pool)
	if err != nil {
		servicesLog.WithError(err).Error("failed to ensure DHCP/DNS services")
		return fmt.Errorf("start dhcp/dns: %w", err)
	}
	servicesLog.WithFields(log.Fields{"dhcp": dhcpName, "dns": dnsName}).Info("DHCP/DNS services ready")

	label := "demo.orch"

	// Resolve bridge name early so VMs and containers can start in parallel.
	var bridgeName string
	if len(o.cfg.VMs) > 0 {
		var err error
		bridgeName, err = o.dc.GetNetworkBridge(netName)
		if err != nil {
			o.log.WithError(err).Error("failed to determine docker bridge")
			return fmt.Errorf("get network bridge: %w", err)
		}

		checkCmd := exec.Command("brctl", "show")
		output, _ := checkCmd.Output()
		if !strings.Contains(string(output), bridgeName) {
			err := fmt.Errorf("Docker bridge %s not found on host", bridgeName)
			o.log.WithField("bridge", bridgeName).Error(err.Error())
			return err
		}

		_ = exec.Command("ip", "link", "set", "dev", bridgeName, "up").Run()
		_ = exec.Command("ip", "link", "set", "dev", bridgeName, "promisc", "on").Run()
		o.log.WithField("bridge", bridgeName).Info("using docker bridge for VMs")
	}

	// ── Launch containers and VMs concurrently ──────────────────────────
	var (
		topWG   sync.WaitGroup
		topErrs []error
		topMu   sync.Mutex
	)

	// --- Container goroutines ---
	if len(o.cfg.Containers) > 0 {
		topWG.Add(1)
		go func() {
			defer topWG.Done()

			o.log.WithFields(log.Fields{"count": len(o.cfg.Containers), "network": netName}).Info("starting containers in parallel")
			var wg sync.WaitGroup
			errChan := make(chan error, len(o.cfg.Containers))

			for _, c := range o.cfg.Containers {
				wg.Add(1)
				go func(container config.ContainerCfg) {
					defer wg.Done()

					entry := o.log.WithField("container", container.Name)
					if err := o.dc.StopAndRemoveByName(container.Name); err != nil {
						entry.WithError(err).Warn("failed to remove existing container")
					}

					var usedIP string
					if container.IP != "" {
						usedIP = container.IP
						if err := o.pool.ReserveIP(usedIP); err != nil {
							entry.WithError(err).Warn("failed to reserve static IP")
						}
					} else {
						ip, err := o.pool.RandomIP()
						if err != nil {
							entry.WithError(err).Error("failed to allocate IP")
							errChan <- fmt.Errorf("alloc ip for %s: %w", container.Name, err)
							return
						}
						usedIP = ip
					}

					entry = entry.WithFields(log.Fields{"ip": usedIP, "image": container.Image})
					entry.Info("starting container")
					if err := o.dc.RunContainer(ctx, container.Name, container.Image, container.Cmd, container.Env, label, usedIP, netName); err != nil {
						entry.WithError(err).Error("container start failed")
						errChan <- fmt.Errorf("run container %s: %w", container.Name, err)
						return
					}
					entry.Info("container started")
				}(c)
			}

			wg.Wait()
			close(errChan)

			var errs []error
			for err := range errChan {
				errs = append(errs, err)
			}
			if len(errs) > 0 {
				joined := errors.Join(errs...)
				o.log.WithError(joined).Error("container provisioning failed")
				topMu.Lock()
				topErrs = append(topErrs, fmt.Errorf("container provisioning errors: %w", joined))
				topMu.Unlock()
				return
			}
			o.log.WithField("count", len(o.cfg.Containers)).Info("all containers started successfully")
		}()
	} else {
		o.log.Info("no containers configured; skipping container launch")
	}

	// --- VM goroutines ---
	if len(o.cfg.VMs) > 0 {
		topWG.Add(1)
		go func() {
			defer topWG.Done()

			o.log.WithField("count", len(o.cfg.VMs)).Info("starting VMs in parallel")
			builder := imagebuilder.NewBuilder()

			var wg sync.WaitGroup
			errChan := make(chan error, len(o.cfg.VMs))

			for _, v := range o.cfg.VMs {
				wg.Add(1)
				go func(vm config.VMCfg) {
					defer wg.Done()

					entry := o.log.WithField("vm", vm.Name)

					if vm.Image == "" {
						entry.Error("image path is required")
						errChan <- fmt.Errorf("VM %s: image path is required", vm.Name)
						return
					}

					memoryMB := vm.MemoryMB
					if memoryMB == 0 {
						memoryMB = 512
					}
					vcpus := vm.VCPUs
					if vcpus == 0 {
						vcpus = 1
					}

					entry = entry.WithFields(log.Fields{"image": vm.Image, "memory_mb": memoryMB, "vcpus": vcpus})

					imagePath := vm.Image
					if len(vm.Packages) > 0 {
						entry.WithField("packages", vm.Packages).Info("building custom image")
						customImage, err := builder.BuildCustomImage(vm.Image, vm.Name, vm.Packages, memoryMB, vcpus)
						if err != nil {
							entry.WithError(err).Error("custom image build failed")
							errChan <- fmt.Errorf("build custom image for %s: %w", vm.Name, err)
							return
						}
						imagePath = customImage
						entry = entry.WithField("custom_image", imagePath)
						entry.Info("custom image ready")
					}

					if o.vc.VMExists(vm.Name) {
						entry.Info("removing existing VM definition")
						if err := o.vc.DestroyVM(vm.Name); err != nil {
							entry.WithError(err).Warn("failed to destroy existing VM")
						}
						if err := o.vc.UndefineVM(vm.Name); err != nil {
							entry.WithError(err).Warn("failed to undefine existing VM")
						}
					}

					entry = entry.WithField("bridge", bridgeName)
					entry.Info("defining VM")
					cloudInitISO := filepath.Join(filepath.Dir(vm.Image), "..", "cloud-init", "cloud-init.iso")
					if _, err := os.Stat(cloudInitISO); err != nil {
						cloudInitISO = "" // no cloud-init ISO found; skip CD-ROM
					}
					if err := o.vc.DefineVM(vm.Name, imagePath, cloudInitISO, bridgeName, memoryMB, vcpus); err != nil {
						entry.WithError(err).Error("define VM failed")
						errChan <- fmt.Errorf("define VM %s: %w", vm.Name, err)
						return
					}

					entry.Info("starting VM")
					if err := o.vc.StartVM(vm.Name); err != nil {
						entry.WithError(err).Error("start VM failed")
						errChan <- fmt.Errorf("start VM %s: %w", vm.Name, err)
						return
					}
					entry.Info("VM started")
				}(v)
			}

			wg.Wait()
			close(errChan)

			var errs []error
			for err := range errChan {
				errs = append(errs, err)
			}
			if len(errs) > 0 {
				joined := errors.Join(errs...)
				o.log.WithError(joined).Error("VM provisioning failed")
				topMu.Lock()
				topErrs = append(topErrs, fmt.Errorf("VM provisioning errors: %w", joined))
				topMu.Unlock()
				return
			}
			o.log.WithField("count", len(o.cfg.VMs)).Info("all VMs started successfully")
		}()
	} else {
		o.log.Info("no VMs configured; skipping VM launch")
	}

	topWG.Wait()

	if len(topErrs) > 0 {
		joined := errors.Join(topErrs...)
		return fmt.Errorf("provisioning failed: %w", joined)
	}

	if o.cfg.Wireguard.Enabled {
		o.log.WithFields(log.Fields{"peer": o.cfg.Wireguard.PeerName, "address": o.cfg.Wireguard.Address}).Info("generating WireGuard client config")
		if err := wg.GenerateClientConfig(o.cfg.Wireguard.PeerName, o.cfg.Wireguard.Address); err != nil {
			o.log.WithError(err).Error("WireGuard client config generation failed")
			return fmt.Errorf("wireguard gen: %w", err)
		}
		o.log.Info("WireGuard client config generated")
	}
	st := state{
		Network:        netName,
		CreatedNetwork: createdNetwork,
		DHCPContainer:  dhcpName,
		DNSContainer:   dnsName,
		TempDir:        tempDir,
		ContainerLabel: label,
	}
	if err := writeState(&st); err != nil {
		return fmt.Errorf("write state: %w", err)
	}

	return nil
}

func (o *Orchestrator) Status(ctx context.Context) (string, error) {
	var b bytes.Buffer

	st, _ := readState()
	net := o.cfg.NetworkName
	if st != nil && st.Network != "" {
		net = st.Network
	} else if o.cfg.AttachTo != "" {
		net = o.cfg.AttachTo
	}
	netInfo, err := o.dc.NetworkInfo(net)
	if err == nil {
		subnet := "N/A"
		if len(netInfo.IPAM.Config) > 0 {
			subnet = netInfo.IPAM.Config[0].Subnet
		}
		fmt.Fprintf(&b, "Docker network: %s (Subnet: %s)\n", netInfo.Name, subnet)
	} else {
		fmt.Fprintf(&b, "Docker network: %s (error: %v)\n", net, err)
	}

	containers, err := o.dc.ListContainersByLabel("demo.orch")
	if err != nil {
		fmt.Fprintf(&b, "List containers error: %v\n", err)
	} else {
		fmt.Fprintf(&b, "\nContainers:\n")
		for _, c := range containers {
			fmt.Fprintf(&b, "  %s: %s\n", strings.Join(c.Names, ","), c.Status)
		}
	}

	// List VMs from config
	if len(o.cfg.VMs) > 0 {
		fmt.Fprintf(&b, "\nVirtual Machines:\n")
		for _, vmCfg := range o.cfg.VMs {
			if o.vc.VMExists(vmCfg.Name) {
				state, err := o.vc.GetVMState(vmCfg.Name)
				if err != nil {
					fmt.Fprintf(&b, "  %s: error getting state (%v)\n", vmCfg.Name, err)
				} else {
					fmt.Fprintf(&b, "  %s: %s", vmCfg.Name, state)

					// Try to get IP if running
					if state == "running" {
						if ip, err := o.vc.GetVMIP(vmCfg.Name); err == nil {
							fmt.Fprintf(&b, " (IP: %s)", ip)
						}
					}
					fmt.Fprintf(&b, "\n")
				}
			} else {
				fmt.Fprintf(&b, "  %s: not defined\n", vmCfg.Name)
			}
		}

		// Add connection instructions
		fmt.Fprintf(&b, "\n📝 VM Access:\n")
		fmt.Fprintf(&b, "  Console: sudo virsh console <vm-name>  (press Ctrl+] to exit)\n")
		fmt.Fprintf(&b, "  SSH: ssh alpine@<vm-ip>  (password: alpine)\n")
		fmt.Fprintf(&b, "  Note: Alpine cloud images may require cloud-init setup for SSH\n")
	}

	return b.String(), nil
}

func (o *Orchestrator) Down(ctx context.Context) error {
	o.log.Info("starting orchestrator cleanup")

	st, err := readState()
	if err != nil {
		cleanupLog := o.log.WithField("label", "demo.orch")
		cleanupLog.Warn("state file not found; attempting cleanup by label")
		if err := o.dc.StopAndRemoveByLabel("demo.orch"); err != nil {
			cleanupLog.WithError(err).Warn("failed to remove containers by label during fallback cleanup")
		} else {
			cleanupLog.Info("removed containers by label")
		}
		return nil
	}

	if st.DHCPContainer != "" {
		entry := o.log.WithField("container", st.DHCPContainer)
		entry.Info("removing DHCP container")
		if err := o.dc.StopAndRemoveByName(st.DHCPContainer); err != nil {
			entry.WithError(err).Warn("failed to remove DHCP container")
		} else {
			entry.Info("DHCP container removed")
		}
	}
	if st.DNSContainer != "" {
		entry := o.log.WithField("container", st.DNSContainer)
		entry.Info("removing DNS container")
		if err := o.dc.StopAndRemoveByName(st.DNSContainer); err != nil {
			entry.WithError(err).Warn("failed to remove DNS container")
		} else {
			entry.Info("DNS container removed")
		}
	}

	label := st.ContainerLabel
	if label == "" {
		label = "demo.orch"
	}
	labelLog := o.log.WithField("label", label)
	labelLog.Info("removing managed containers by label")
	if err := o.dc.StopAndRemoveByLabel(label); err != nil {
		labelLog.WithError(err).Warn("failed to remove containers by label")
	} else {
		labelLog.Info("containers removed by label")
	}

	if len(o.cfg.VMs) > 0 {
		o.log.WithField("count", len(o.cfg.VMs)).Info("stopping and undefining VMs")
		for _, v := range o.cfg.VMs {
			entry := o.log.WithField("vm", v.Name)
			entry.Info("stopping VM")
			if err := o.vc.ShutdownVM(v.Name); err != nil {
				entry.WithError(err).Warn("graceful shutdown failed")
			}
			if err := o.vc.DestroyVM(v.Name); err != nil {
				entry.WithError(err).Warn("force destroy failed")
			}
			if err := o.vc.UndefineVM(v.Name); err != nil {
				entry.WithError(err).Warn("undefine failed")
			} else {
				entry.Info("VM undefined")
			}
		}
	}

	if st.CreatedNetwork && st.Network != "" {
		netLog := o.log.WithField("network", st.Network)
		netLog.Info("removing managed network")
		if err := o.dc.RemoveNetwork(st.Network); err != nil {
			netLog.WithError(err).Warn("failed to remove network")
		} else {
			netLog.Info("network removed")
		}
	}

	if st.TempDir != "" {
		tempLog := o.log.WithField("path", st.TempDir)
		tempLog.Info("removing temporary directory")
		if err := os.RemoveAll(st.TempDir); err != nil {
			tempLog.WithError(err).Warn("failed to remove temporary directory")
		} else {
			tempLog.Info("temporary directory removed")
		}
	}

	if err := os.Remove(stateFile); err != nil {
		o.log.WithError(err).Warn("failed to remove state file")
	} else {
		o.log.WithField("state_file", stateFile).Info("state file removed")
	}

	o.log.Info("cleanup completed")
	return nil
}

// state helpers

func writeState(s *state) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(stateFile, b, 0644)
}

func readState() (*state, error) {
	b, err := os.ReadFile(stateFile)
	if err != nil {
		return nil, err
	}
	var s state
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
