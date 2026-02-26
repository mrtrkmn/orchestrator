package imagebuilder

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mrtrkmn/orchestrator/orch/libvirtctl"
)

type Builder struct{}

func NewBuilder() *Builder {
	return &Builder{}
}

// BuildCustomImage creates a custom image with packages installed
func (b *Builder) BuildCustomImage(baseImage string, vmName string, packages []string, memoryMB, vcpus int) (string, error) {
	if len(packages) == 0 {
		return baseImage, nil
	}

	// Generate custom image path
	customImagePath := strings.TrimSuffix(baseImage, filepath.Ext(baseImage)) + "-" + vmName + "-custom.qcow2"

	// Check if custom image already exists
	if _, err := os.Stat(customImagePath); err == nil {
		fmt.Printf("Using existing custom image: %s\n", customImagePath)
		return customImagePath, nil
	}

	fmt.Printf("Creating custom image with packages: %v\n", packages)
	fmt.Println("This will take a few minutes...")

	// Create a copy of base image
	fmt.Println("Step 1/5: Copying base image...")
	copyCmd := exec.Command("cp", baseImage, customImagePath)
	if err := copyCmd.Run(); err != nil {
		return "", fmt.Errorf("copy base image: %w", err)
	}

	// Fix permissions for libvirt access
	chownCmd := exec.Command("sudo", "chown", "libvirt-qemu:kvm", customImagePath)
	if err := chownCmd.Run(); err != nil {
		return "", fmt.Errorf("chown custom image: %w", err)
	}
	
	chmodCmd := exec.Command("sudo", "chmod", "644", customImagePath)
	if err := chmodCmd.Run(); err != nil {
		return "", fmt.Errorf("chmod custom image: %w", err)
	}

	// Create cloud-init with package installation
	fmt.Println("Step 2/5: Creating cloud-init configuration...")
	tmpDir := filepath.Join(filepath.Dir(customImagePath), ".builder-tmp")
	os.RemoveAll(tmpDir) // Clean up any old builder directory
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return "", fmt.Errorf("create builder dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create user-data with package installation
	userData := fmt.Sprintf(`#cloud-config
users:
  - name: debian
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    lock_passwd: false
    passwd: $6$rounds=4096$saltsaltsalt$6F.KFjCVyHjGPqsJVBLp6gjLhQ7KqnPqzCQQpjQEXxWvEwS5KpMNJN4oKe2oLPKqCzUE/8VNl8kLvmQzPHPqh0

ssh_pwauth: true

chpasswd:
  list: |
    root:root
  expire: false

packages:
%s

runcmd:
  - systemctl enable ssh
  - systemctl start ssh
  - touch /root/packages-installed

power_state:
  mode: poweroff
  timeout: 300
  condition: True
`, generatePackageList(packages))

	userDataPath := filepath.Join(tmpDir, "user-data")
	if err := ioutil.WriteFile(userDataPath, []byte(userData), 0644); err != nil {
		return "", fmt.Errorf("write user-data: %w", err)
	}

	metaData := fmt.Sprintf("instance-id: %s-builder\nlocal-hostname: %s-builder\n", vmName, vmName)
	metaDataPath := filepath.Join(tmpDir, "meta-data")
	if err := ioutil.WriteFile(metaDataPath, []byte(metaData), 0644); err != nil {
		return "", fmt.Errorf("write meta-data: %w", err)
	}

	// Generate cloud-init ISO
	cloudInitISO := filepath.Join(tmpDir, "cloud-init.iso")
	isoCmd := exec.Command("genisoimage", "-output", cloudInitISO, "-volid", "cidata", "-joliet", "-rock", userDataPath, metaDataPath)
	if err := isoCmd.Run(); err != nil {
		return "", fmt.Errorf("create cloud-init ISO: %w", err)
	}

	// Define temporary VM with NAT networking for internet access
	fmt.Println("Step 3/5: Creating temporary VM for package installation...")
	virtType := "kvm"
	if !libvirtctl.KVMAvailable() {
		virtType = "qemu"
	}
	vmXML := fmt.Sprintf(`<domain type='%s'>
  <name>%s-builder</name>
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
    </disk>
    <disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='%s'/>
      <target dev='sdb' bus='sata'/>
      <readonly/>
    </disk>
    <interface type='network'>
      <source network='default'/>
      <model type='virtio'/>
    </interface>
    <serial type='pty'>
      <target port='0'/>
    </serial>
    <console type='pty'>
      <target type='serial' port='0'/>
    </console>
  </devices>
</domain>`, virtType, vmName, memoryMB, vcpus, customImagePath, cloudInitISO)

	// Cleanup existing builder VM if it exists
	exec.Command("sudo", "virsh", "destroy", fmt.Sprintf("%s-builder", vmName)).Run()
	exec.Command("sudo", "virsh", "undefine", fmt.Sprintf("%s-builder", vmName)).Run()

	defineCmd := exec.Command("sudo", "virsh", "define", "/dev/stdin")
	defineCmd.Stdin = strings.NewReader(vmXML)
	if output, err := defineCmd.CombinedOutput(); err != nil {
		// If VM already exists, undefine it first
		if strings.Contains(string(output), "already exists") {
			fmt.Println("Removing existing builder VM...")
			exec.Command("sudo", "virsh", "destroy", fmt.Sprintf("%s-builder", vmName)).Run()
			exec.Command("sudo", "virsh", "undefine", fmt.Sprintf("%s-builder", vmName)).Run()
			// Try defining again
			defineCmd = exec.Command("sudo", "virsh", "define", "/dev/stdin")
			defineCmd.Stdin = strings.NewReader(vmXML)
			if output, err := defineCmd.CombinedOutput(); err != nil {
				return "", fmt.Errorf("define builder VM (retry): %w, output: %s", err, output)
			}
		} else {
			return "", fmt.Errorf("define builder VM: %w, output: %s", err, output)
		}
	}

	// Ensure default network exists and is active
	if err := ensureDefaultNetwork(); err != nil {
		return "", fmt.Errorf("ensure default network: %w", err)
	}

	// Start VM
	fmt.Println("Step 4/5: Installing packages (this may take several minutes)...")
	startCmd := exec.Command("sudo", "virsh", "start", fmt.Sprintf("%s-builder", vmName))
	if output, err := startCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("start builder VM: %w, output: %s", err, output)
	}

	// Wait for VM to shutdown (cloud-init will power off after installation)
	fmt.Println("Waiting for package installation to complete...")
	maxWait := 10 * time.Minute
	checkInterval := 5 * time.Second
	elapsed := time.Duration(0)

	for elapsed < maxWait {
		time.Sleep(checkInterval)
		elapsed += checkInterval

		// Check VM state
		stateCmd := exec.Command("sudo", "virsh", "domstate", fmt.Sprintf("%s-builder", vmName))
		output, err := stateCmd.Output()
		if err == nil {
			state := strings.TrimSpace(string(output))
			if state == "shut off" {
				fmt.Println("Step 5/5: Package installation complete!")
				break
			}
			fmt.Printf("Progress: VM running, installing packages... (%v elapsed)\n", elapsed)
		}

		if elapsed >= maxWait {
			// Force shutdown
			exec.Command("sudo", "virsh", "destroy", fmt.Sprintf("%s-builder", vmName)).Run()
			return "", fmt.Errorf("timeout waiting for package installation")
		}
	}

	// Cleanup builder VM
	undefineCmd := exec.Command("sudo", "virsh", "undefine", fmt.Sprintf("%s-builder", vmName))
	undefineCmd.Run()

	fmt.Printf("Custom image created successfully: %s\n", customImagePath)
	return customImagePath, nil
}

func generatePackageList(packages []string) string {
	var lines []string
	for _, pkg := range packages {
		lines = append(lines, fmt.Sprintf("  - %s", pkg))
	}
	return strings.Join(lines, "\n")
}

// ensureDefaultNetwork ensures the libvirt default network exists and is active
func ensureDefaultNetwork() error {
	// Check if default network exists
	checkCmd := exec.Command("sudo", "virsh", "net-info", "default")
	if err := checkCmd.Run(); err != nil {
		// Network doesn't exist, create it
		fmt.Println("Creating libvirt default network for internet access...")
		networkXML := `<network>
  <name>default</name>
  <forward mode='nat'/>
  <bridge name='virbr0' stp='on' delay='0'/>
  <ip address='192.168.122.1' netmask='255.255.255.0'>
    <dhcp>
      <range start='192.168.122.2' end='192.168.122.254'/>
    </dhcp>
  </ip>
</network>`
		
		defineCmd := exec.Command("sudo", "virsh", "net-define", "/dev/stdin")
		defineCmd.Stdin = strings.NewReader(networkXML)
		if output, err := defineCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("define default network: %w, output: %s", err, output)
		}
		
		// Mark it for autostart
		autostartCmd := exec.Command("sudo", "virsh", "net-autostart", "default")
		autostartCmd.Run()
	}
	
	// Start the network if not active
	startCmd := exec.Command("sudo", "virsh", "net-start", "default")
	output, err := startCmd.CombinedOutput()
	if err != nil {
		// Ignore "already started" errors
		if !strings.Contains(string(output), "already active") {
			return fmt.Errorf("start default network: %w, output: %s", err, output)
		}
	}
	
	return nil
}

