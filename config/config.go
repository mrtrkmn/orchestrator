package config

import (
	"io/ioutil"

	"gopkg.in/yaml.v3"
)

type ContainerCfg struct {
	Name  string   `yaml:"name"`
	Image string   `yaml:"image"`
	Cmd   []string `yaml:"cmd,omitempty"`
	Env   []string `yaml:"env,omitempty"`
	Label string   `yaml:"label,omitempty"`
	IP    string   `yaml:"ip,omitempty"` // optional static IP inside network
}

type VMCfg struct {
	Name     string   `yaml:"name"`
	Image    string   `yaml:"image"`     // path to qcow2 image for libvirt
	MemoryMB int      `yaml:"memory_mb"` // memory in MB
	VCPUs    int      `yaml:"vcpus"`     // number of virtual CPUs
	Packages []string `yaml:"packages,omitempty"` // packages to install (creates custom image)
}

type NetworkType string

const (
	NetworkBridge  NetworkType = "bridge"
	NetworkMacVlan NetworkType = "macvlan"
)

type Config struct {
	NetworkName string         `yaml:"network_name"`
	Subnet      string         `yaml:"subnet"` // e.g. 172.18.0.0/24
	NetworkType NetworkType    `yaml:"network_type,omitempty"`
	AttachTo    string         `yaml:"attach_to,omitempty"` // attach to existing network name
	Containers  []ContainerCfg `yaml:"containers,omitempty"`
	VMs         []VMCfg        `yaml:"vms,omitempty"`
	Wireguard   struct {
		Enabled  bool   `yaml:"enabled"`
		PeerName string `yaml:"peer_name,omitempty"`
		Address  string `yaml:"address,omitempty"` // e.g. 10.10.0.2/24
	} `yaml:"wireguard,omitempty"`
}

func LoadConfig(path string) (*Config, error) {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}
