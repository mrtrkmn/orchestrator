package dockerctl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/mrtrkmn/orchestrator/orch/ipool"
)

type Client struct {
	c *docker.Client
}

func NewClient() (*Client, error) {
	c, err := docker.NewClientFromEnv()
	if err != nil {
		return nil, err
	}
	_ = c.Ping()
	return &Client{c: c}, nil
}

func (c *Client) CreateNetwork(name, subnet, driver string) error {
	opts := docker.CreateNetworkOptions{
		Name:   name,
		Driver: driver,
		IPAM: &docker.IPAMOptions{
			Config: []docker.IPAMConfig{{Subnet: subnet}},
		},
		Labels: map[string]string{"orch": "true"},
	}
	_, err := c.c.CreateNetwork(opts)
	return err
}

func (c *Client) NetworkInfo(name string) (*docker.Network, error) {
	return c.c.NetworkInfo(name)
}

func (c *Client) GetNetworkBridge(name string) (string, error) {
	info, err := c.c.NetworkInfo(name)
	if err != nil {
		return "", err
	}
	if info.Options != nil {
		if bridge, ok := info.Options["com.docker.network.bridge.name"]; ok && bridge != "" {
			return bridge, nil
		}
	}
	if info.Driver == "bridge" && info.ID != "" {
		id := info.ID
		if len(id) > 12 {
			id = id[:12]
		}
		return fmt.Sprintf("br-%s", id), nil
	}
	return "", fmt.Errorf("bridge name not found for network %s", name)
}

func (c *Client) pullImageWithRetries(image string, retry int) error {
	var lastErr error
	for i := 0; i < retry; i++ {
		opts := docker.PullImageOptions{
			Repository:   image,
			OutputStream: io.Discard,
		}
		err := c.c.PullImage(opts, docker.AuthConfiguration{})
		if err == nil {
			return nil
		}
		lastErr = err
		time.Sleep(time.Duration(2<<i) * time.Second)
	}
	return fmt.Errorf("pull image %s failed: %w", image, lastErr)
}

func (c *Client) RunContainer(ctx context.Context, name, image string, cmd []string, env []string, label, ip, network string) error {
	if err := c.pullImageWithRetries(image, 3); err != nil {
		_, inspectErr := c.c.InspectImage(image)
		if inspectErr != nil {
			return err
		}
	}

	cfg := &docker.Config{
		Image:  image,
		Cmd:    cmd,
		Env:    env,
		Labels: map[string]string{},
	}
	if label != "" {
		cfg.Labels[label] = "true"
	} else {
		cfg.Labels["demo.orch"] = "true"
	}

	hc := &docker.HostConfig{}
	createOpts := docker.CreateContainerOptions{
		Name:       name,
		Config:     cfg,
		HostConfig: hc,
	}
	cont, err := c.c.CreateContainer(createOpts)
	if err != nil {
		if strings.Contains(err.Error(), "Conflict.") || strings.Contains(err.Error(), "is already in use by container") {
			cs, listErr := c.c.ListContainers(docker.ListContainersOptions{All: true, Filters: map[string][]string{"name": {name}}})
			if listErr == nil && len(cs) > 0 {
				existing, inspectErr := c.c.InspectContainer(cs[0].ID)
				if inspectErr == nil && existing.State.Running {
					return nil
				}
				if startErr := c.c.StartContainer(cs[0].ID, nil); startErr != nil && !strings.Contains(startErr.Error(), "already running") && !strings.Contains(startErr.Error(), "already started") {
					return startErr
				}
				return c.waitForRunning(ctx, cs[0].ID, 20*time.Second)
			}
			return nil
		}
		return err
	}

	if network != "" {
		connOpt := docker.NetworkConnectionOptions{Container: cont.ID}
		if ip != "" {
			connOpt.EndpointConfig = &docker.EndpointConfig{IPAMConfig: &docker.EndpointIPAMConfig{IPv4Address: ip}}
		}
		if err := c.c.ConnectNetwork(network, connOpt); err != nil {
			_ = c.c.RemoveContainer(docker.RemoveContainerOptions{ID: cont.ID, Force: true})
			return fmt.Errorf("connect container %s to network %s: %w", name, network, err)
		}
	}

	if err := c.c.StartContainer(cont.ID, nil); err != nil {
		return err
	}

	return c.waitForRunning(ctx, cont.ID, 20*time.Second)
}

func (c *Client) ListContainersByLabel(label string) ([]docker.APIContainers, error) {
	return c.c.ListContainers(docker.ListContainersOptions{
		All: true,
		Filters: map[string][]string{
			"label": {label},
		},
	})
}

func (c *Client) StopAndRemoveByLabel(label string) error {
	containers, err := c.ListContainersByLabel(label)
	if err != nil {
		return err
	}
	for _, cont := range containers {
		_ = c.c.StopContainer(cont.ID, 10)
		_ = c.c.RemoveContainer(docker.RemoveContainerOptions{ID: cont.ID, Force: true})
	}
	return nil
}

func (c *Client) StopAndRemoveByName(name string) error {
	cs, err := c.c.ListContainers(docker.ListContainersOptions{All: true, Filters: map[string][]string{"name": {name}}})
	if err != nil {
		return err
	}
	for _, cont := range cs {
		_ = c.c.StopContainer(cont.ID, 10)
		_ = c.c.RemoveContainer(docker.RemoveContainerOptions{ID: cont.ID, Force: true})
	}
	return nil
}

func (c *Client) RemoveNetwork(name string) error {
	return c.c.RemoveNetwork(name)
}

func (c *Client) EnsureDHCPAndDNS(ctx context.Context, network string, pool *ipool.IPPool) (string, string, string, error) {
	tempDir, err := os.MkdirTemp("", "orch-dhcp-dns")
	if err != nil {
		return "", "", "", err
	}
	cleanup := func() {
		_ = os.RemoveAll(tempDir)
	}

	_, ipNet, err := net.ParseCIDR(pool.Subnet())
	if err != nil {
		cleanup()
		return "", "", "", err
	}
	netIP := ipNet.IP.To4()
	if netIP == nil {
		cleanup()
		return "", "", "", fmt.Errorf("subnet %s is not IPv4", pool.Subnet())
	}
	mask := ipNet.Mask
	if len(mask) != 4 {
		cleanup()
		return "", "", "", fmt.Errorf("unexpected mask for subnet %s", pool.Subnet())
	}
	maskStr := fmt.Sprintf("%d.%d.%d.%d", mask[0], mask[1], mask[2], mask[3])
	networkAddr := netIP.String()

	broadcastBytes := make([]byte, 4)
	for i := 0; i < 4; i++ {
		broadcastBytes[i] = netIP[i] | ^mask[i]
	}
	broadcast := net.IP(broadcastBytes).String()

	dhcpIP := pool.FormatIP(2)
	dnsIP := pool.FormatIP(3)
	minRange := pool.FormatIP(4)
	maxRange := pool.FormatIP(pool.BroadcastOffset() - 1) // last usable host before broadcast
	gatewayIP := pool.FormatIP(1)

	for _, infraIP := range []string{dhcpIP, dnsIP} {
		if reserveErr := pool.ReserveIP(infraIP); reserveErr != nil && !strings.Contains(reserveErr.Error(), "already reserved") {
			cleanup()
			return "", "", "", reserveErr
		}
	}
	success := false
	defer func() {
		if !success {
			for _, infraIP := range []string{dhcpIP, dnsIP} {
				_ = pool.ReleaseIP(infraIP)
			}
		}
	}()

	dhcpConfPath := filepath.Join(tempDir, "dhcpd.conf")
	dnsCorePath := filepath.Join(tempDir, "Corefile")
	dnsZonePath := filepath.Join(tempDir, "zonefile")

	dhcpConf := fmt.Sprintf(`default-lease-time 600;
max-lease-time 7200;
authoritative;
option domain-name-servers %s;
option routers %s;

subnet %s netmask %s {
  range %s %s;
  option subnet-mask %s;
  option broadcast-address %s;
}
`, dnsIP, gatewayIP, networkAddr, maskStr, minRange, maxRange, maskStr, broadcast)

	if err := os.WriteFile(dhcpConfPath, []byte(dhcpConf), 0o644); err != nil {
		cleanup()
		return "", "", "", err
	}

	core := `. {
    file zonefile
    errors
    log
}`
	if err := os.WriteFile(dnsCorePath, []byte(core), 0o644); err != nil {
		cleanup()
		return "", "", "", err
	}

	zone := fmt.Sprintf(`$ORIGIN local.
@ 3600 IN SOA ns.local. hostmaster.local. (
    2024010101
    7200
    3600
    1209600
    3600
)
  3600 IN NS ns.local.
ns 3600 IN A %s
`, dnsIP)
	if err := os.WriteFile(dnsZonePath, []byte(zone), 0o644); err != nil {
		cleanup()
		return "", "", "", err
	}

	safeNet := strings.ReplaceAll(network, "/", "-")
	dhcpName := fmt.Sprintf("orch-dhcp-%s", safeNet)
	dnsName := fmt.Sprintf("orch-dns-%s", safeNet)

	_ = c.StopAndRemoveByName(dhcpName)
	_ = c.StopAndRemoveByName(dnsName)

	const dhcpImage = "networkboot/dhcpd:1.2.0"
	const dnsImage = "coredns/coredns:1.6.1"

	if err := c.pullImageWithRetries(dhcpImage, 2); err != nil {
		if _, inspectErr := c.c.InspectImage(dhcpImage); inspectErr != nil {
			cleanup()
			return "", "", "", err
		}
	}
	if err := c.pullImageWithRetries(dnsImage, 2); err != nil {
		if _, inspectErr := c.c.InspectImage(dnsImage); inspectErr != nil {
			cleanup()
			return "", "", "", err
		}
	}

	dhcpOpts := docker.CreateContainerOptions{
		Name: dhcpName,
		Config: &docker.Config{
			Image: dhcpImage,
			Cmd:   []string{"eth0"},
			Labels: map[string]string{
				"demo.orch": "true",
				"service":   "dhcp",
			},
		},
		HostConfig: &docker.HostConfig{
				NetworkMode: network,
				CapAdd:      []string{"NET_ADMIN"},
				Binds: []string{
					fmt.Sprintf("%s:/data", tempDir),
				},
		},
		NetworkingConfig: &docker.NetworkingConfig{
			EndpointsConfig: map[string]*docker.EndpointConfig{
				network: {
					IPAMConfig: &docker.EndpointIPAMConfig{
						IPv4Address: dhcpIP,
					},
				},
			},
		},
	}

	dnsOpts := docker.CreateContainerOptions{
		Name: dnsName,
		Config: &docker.Config{
			Image: dnsImage,
			Cmd:   []string{"--conf", "Corefile"},
			Labels: map[string]string{
				"demo.orch": "true",
				"service":   "dns",
			},
		},
		HostConfig: &docker.HostConfig{
			NetworkMode: network,
			Binds: []string{
				fmt.Sprintf("%s:/Corefile:ro", dnsCorePath),
				fmt.Sprintf("%s:/zonefile:ro", dnsZonePath),
			},
		},
		NetworkingConfig: &docker.NetworkingConfig{
			EndpointsConfig: map[string]*docker.EndpointConfig{
				network: {
					IPAMConfig: &docker.EndpointIPAMConfig{
						IPv4Address: dnsIP,
					},
				},
			},
		},
	}

	type serviceResult struct {
		name string
		id   string
		err  error
	}

	services := []struct {
		name string
		opts docker.CreateContainerOptions
	}{
		{name: dhcpName, opts: dhcpOpts},
		{name: dnsName, opts: dnsOpts},
	}

	results := make(chan serviceResult, len(services))
	var wg sync.WaitGroup
	wg.Add(len(services))
	for _, svc := range services {
		svcCopy := svc
		go func() {
			defer wg.Done()
			id, svcErr := c.createStartAndWait(ctx, svcCopy.opts, 30*time.Second)
			results <- serviceResult{name: svcCopy.name, id: id, err: svcErr}
		}()
	}
	wg.Wait()
	close(results)

	var (
		errs       []error
		successIDs []string
	)
	for res := range results {
		if res.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", res.name, res.err))
			continue
		}
		successIDs = append(successIDs, res.id)
	}

	if len(errs) > 0 {
		for _, id := range successIDs {
			_ = c.c.RemoveContainer(docker.RemoveContainerOptions{ID: id, Force: true})
		}
		cleanup()
		return "", "", "", errors.Join(errs...)
	}

	success = true
	return dhcpName, dnsName, tempDir, nil
}

func (c *Client) createStartAndWait(ctx context.Context, opts docker.CreateContainerOptions, wait time.Duration) (string, error) {
	container, err := c.c.CreateContainer(opts)
	if err != nil {
		return "", err
	}

	if err := c.c.StartContainer(container.ID, nil); err != nil {
		_ = c.c.RemoveContainer(docker.RemoveContainerOptions{ID: container.ID, Force: true})
		return "", err
	}

	if err := c.waitForRunning(ctx, container.ID, wait); err != nil {
		_ = c.c.RemoveContainer(docker.RemoveContainerOptions{ID: container.ID, Force: true})
		return "", err
	}

	return container.ID, nil
}

func (c *Client) waitForRunning(ctx context.Context, containerID string, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return errors.New("timeout waiting for container to be running")
		case <-ticker.C:
			ins, err := c.c.InspectContainer(containerID)
			if err != nil {
				continue
			}
			if ins.State.Running {
				return nil
			}
		}
	}
}
