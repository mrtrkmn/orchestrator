package libvirtctl

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type Client struct{}

func NewClient() *Client {
	return &Client{}
}

// KVMAvailable checks whether /dev/kvm exists, indicating hardware
// virtualisation support. When it returns false callers should use
// the "qemu" domain type instead of "kvm".
func KVMAvailable() bool {
	_, err := os.Stat("/dev/kvm")
	return err == nil
}

// virtType returns "kvm" when hardware acceleration is available,
// otherwise "qemu" (software emulation).
func virtType() string {
	if KVMAvailable() {
		return "kvm"
	}
	return "qemu"
}

// DefineVM creates a VM from a qcow2 image.
// cloudInitISO may be empty; when set the ISO is attached as a CD-ROM.
func (c *Client) DefineVM(name, imagePath, cloudInitISO, network string, memoryMB, vcpus int) error {
	cdromXML := ""
	if cloudInitISO != "" {
		cdromXML = fmt.Sprintf(`
    <disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='%s'/>
      <target dev='sdb' bus='sata'/>
      <readonly/>
    </disk>`, cloudInitISO)
	}

	// Create VM XML definition (headless, no graphics)
	xml := fmt.Sprintf(`<domain type='%s'>
  <name>%s</name>
  <memory unit='MiB'>%d</memory>
  <vcpu>%d</vcpu>
  <os>
    <type arch='x86_64' machine='pc'>hvm</type>
    <boot dev='hd'/>
  </os>
  <devices>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='%s'/>
      <target dev='vda' bus='virtio'/>
    </disk>%s
    <interface type='bridge'>
      <source bridge='%s'/>
      <model type='virtio'/>
    </interface>
    <serial type='pty'>
      <target port='0'/>
    </serial>
    <console type='pty'>
      <target type='serial' port='0'/>
    </console>
  </devices>
</domain>`, virtType(), name, memoryMB, vcpus, imagePath, cdromXML, network)

	cmd := exec.Command("virsh", "define", "/dev/stdin")
	cmd.Stdin = strings.NewReader(xml)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("define VM: %w, output: %s", err, output)
	}
	return nil
}

// StartVM starts a libvirt VM
func (c *Client) StartVM(name string) error {
	cmd := exec.Command("virsh", "start", name)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Ignore "already started" errors
		if strings.Contains(string(output), "already active") {
			return nil
		}
		return fmt.Errorf("start VM: %w, output: %s", err, output)
	}
	return nil
}

// ShutdownVM gracefully shuts down a VM
func (c *Client) ShutdownVM(name string) error {
	cmd := exec.Command("virsh", "shutdown", name)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Ignore if VM is already shut down
		if strings.Contains(string(output), "not running") {
			return nil
		}
		return fmt.Errorf("shutdown VM: %w, output: %s", err, output)
	}
	return nil
}

// DestroyVM forcefully stops a VM
func (c *Client) DestroyVM(name string) error {
	cmd := exec.Command("virsh", "destroy", name)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Ignore if VM is not running
		if strings.Contains(string(output), "not running") {
			return nil
		}
		return fmt.Errorf("destroy VM: %w, output: %s", err, output)
	}
	return nil
}

// UndefineVM removes a VM definition
func (c *Client) UndefineVM(name string) error {
	cmd := exec.Command("virsh", "undefine", name)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("undefine VM: %w, output: %s", err, output)
	}
	return nil
}

// VMExists checks if a VM is defined
func (c *Client) VMExists(name string) bool {
	cmd := exec.Command("virsh", "dominfo", name)
	err := cmd.Run()
	return err == nil
}

// GetVMState returns the state of a VM
func (c *Client) GetVMState(name string) (string, error) {
	cmd := exec.Command("virsh", "domstate", name)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("get VM state: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// GetVMIP attempts to get the IP address of a VM from DHCP leases
func (c *Client) GetVMIP(name string) (string, error) {
	cmd := exec.Command("virsh", "domifaddr", name, "--source", "agent")
	output, err := cmd.Output()
	if err != nil {
		// Try lease source instead
		cmd = exec.Command("virsh", "domifaddr", name, "--source", "lease")
		output, err = cmd.Output()
		if err != nil {
			return "", fmt.Errorf("get VM IP: %w", err)
		}
	}
	
	// Parse output for IP address
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "ipv4") {
			fields := strings.Fields(line)
			if len(fields) >= 4 {
				// IP is in format like "192.168.122.100/24"
				ipWithMask := fields[3]
				ip := strings.Split(ipWithMask, "/")[0]
				return ip, nil
			}
		}
	}
	
	return "", fmt.Errorf("no IP address found")
}

// ListVMs lists all VMs
func (c *Client) ListVMs() ([]string, error) {
	cmd := exec.Command("virsh", "list", "--all", "--name")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list VMs: %w", err)
	}
	
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var vms []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			vms = append(vms, line)
		}
	}
	return vms, nil
}

// CreateDockerBridge creates a bridge interface for Docker network integration
func (c *Client) CreateDockerBridge(bridgeName, dockerNetwork string) error {
	// Check if bridge already exists in Docker
	cmd := exec.Command("docker", "network", "inspect", dockerNetwork, "-f", "{{.Options.\"com.docker.network.bridge.name\"}}")
	output, err := cmd.Output()
	if err == nil && strings.TrimSpace(string(output)) != "" {
		// Docker network already has a bridge
		return nil
	}
	
	// Create a new Docker network with custom bridge name if needed
	cmd = exec.Command("docker", "network", "create",
		"--driver", "bridge",
		"--opt", fmt.Sprintf("com.docker.network.bridge.name=%s", bridgeName),
		dockerNetwork)
	output, err = cmd.CombinedOutput()
	if err != nil {
		if !strings.Contains(string(output), "already exists") {
			return fmt.Errorf("create docker bridge: %w, output: %s", err, output)
		}
	}
	return nil
}
