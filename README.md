# Orchestrating a Virtual Hub in Go

> **Conference talk** — *Gophers İstanbul 2026*
> by [Ahmet Türkmen](https://www.linkedin.com/in/mrturkmen/) (Systems Development Engineer @ AWS)

A single Go binary that provisions Docker containers, libvirt/KVM virtual machines, networking (DHCP + DNS), and WireGuard VPN — all from one YAML file. Built as a real-world showcase of Go's strengths in infrastructure tooling.

```
┌─── Sunucu ──────────────────────────────────────────────────────┐
│                                                                 │
│   ┌──────────┐  ┌──────────┐  ┌──────────┐   ┌──────────────┐  │
│   │  nginx   │  │  whoami  │  │ DHCP/DNS │   │  Debian VM   │  │
│   │ :alpine  │  │  :latest │  │ container│   │  cloud-init  │  │
│   │ .5.10    │  │ .5.11    │  │ .5.2/.3  │   │  1024MB RAM  │  │
│   └────┬─────┘  └────┬─────┘  └────┬─────┘   └──────┬───────┘  │
│        └──────────────┴─────────────┴────────────────┘          │
│              Docker Bridge Ağı: 172.19.5.0/24                   │
│        ┌──────────────────────┐                                 │
│        │  WireGuard VPN       │                                 │
│        │  10.10.0.0/24        │                                 │
│        └────────┬─────────────┘                                 │
└─────────────────┼───────────────────────────────────────────────┘
                  │
            ┌─────┴─────┐
            │  Remote   │
            │  Client   │
            └───────────┘
```

---

## Why Go for Infrastructure?

This project demonstrates **five Go features** that make it a natural fit for infrastructure tooling:

| Go Feature | How It's Used | Source |
|---|---|---|
| **`net`** | CIDR parsing, uint32 IP arithmetic, broadcast calculation, DHCP config generation | `orch/ipool/`, `orch/dockerctl/` |
| **`goroutine`** | Two-level fan-out: containers and VMs provision in parallel | `orch/orchestrator.go` |
| **`channel`** | Buffered error channels, typed result channels, `select` multiplexing | `orch/orchestrator.go`, `orch/dockerctl/` |
| **`os/exec`** | virsh CLI piping via stdin, KVM detection with `os.Stat`, cloud-init ISO handling | `orch/libvirtctl/` |
| **`sync`** | Thread-safe IP pool with dual strategy (eager map ≤1024 hosts / lazy probe >1024) | `orch/ipool/` |
| **`crypto`** | Pure-Go WireGuard X25519 key generation — no CGo, no external tools | `wg/` |

---

## Quick Start

```bash
# Prerequisites: Go 1.18+, Docker, libvirt/QEMU
# Ubuntu setup:
sudo ./scripts/setup-ubuntu.sh
./scripts/setup-bridge-helper.sh
# Log out and back in after bridge helper setup

# Build (single static binary, ~15 MB)
go build -o orchestrator .

# Bring everything up
sudo ./orchestrator up -c config/example.yaml

# Check status
sudo ./orchestrator status

# SSH into the VM
sudo ./orchestrator ssh demo-vm

# Tear everything down
sudo ./orchestrator down
```

## Configuration

Everything is defined in a single YAML file:

```yaml
network_name: demo-net
subnet: 172.19.5.0/24
network_type: bridge

containers:
  - name: web-demo
    image: nginx:alpine
    ip: 172.19.5.10

  - name: whoami
    image: containous/whoami:latest
    ip: 172.19.5.11

vms:
  - name: demo-vm
    image: ./images/debian-12.qcow2
    memory_mb: 1024
    vcpus: 2
    packages: [curl, wget, vim, htop, net-tools, nmap]

wireguard:
  enabled: true
  peer_name: demo-client
  address: 10.10.0.2/24
```

## What Happens on `orchestrator up`?

```
1.  Parse YAML config
2.  Create Docker bridge network (172.19.5.0/24)
3.  Initialize IP pool (/8 → /30 CIDR support)
4.  Reserve DHCP (.2) and DNS (.3) IPs
5.  Start DHCP + DNS containers (parallel)      ← goroutine + channel
6.  ┌── Start containers (parallel)              ← goroutine fan-out
    │   ├── nginx:alpine     → 172.19.5.10
    │   └── whoami           → 172.19.5.11
    └── Start VMs (parallel)                     ← goroutine fan-out
        └── demo-vm          → DHCP assigned
7.  Generate WireGuard client config             ← pure-Go crypto
8.  Write state file (JSON)
```

## Project Structure

```
orchestrator/
├── main.go                 # entry point
├── cmd/                    # CLI commands (Cobra)
│   ├── root.go             # flags, logging setup
│   ├── up.go / down.go     # lifecycle
│   ├── status.go           # resource status
│   ├── ssh.go              # interactive SSH to VMs
│   └── log_hook.go         # goroutine-ID logging
├── config/                 # YAML parser + example config
├── orch/                   # core orchestrator
│   ├── orchestrator.go     # Up / Down / Status
│   ├── dockerctl/          # Docker API wrapper
│   ├── libvirtctl/         # virsh CLI wrapper (KVM/QEMU)
│   ├── imagebuilder/       # custom VM image builder
│   └── ipool/              # thread-safe IP pool (dual strategy)
├── wg/                     # WireGuard config generator
├── scripts/                # setup & helper scripts
├── cloud-init/             # VM cloud-init data
└── systemd/                # systemd service unit
```

## Key Design Decisions

- **Graceful fallback** — no `/dev/kvm`? Falls back to QEMU emulation. No cloud-init ISO? Skips CD-ROM.
- **IPv4 as uint32** — all IP arithmetic is integer comparison; no string parsing in hot paths.
- **Dual-strategy IP pool** — eager (pre-built map) for ≤1024 hosts, lazy (random probe) for larger subnets. Benchmark: 10.0.0.0/8 pool creation went from 5.1s → 0.004s.
- **Error chaining** — `fmt.Errorf("%w")` at every layer; `errors.Join()` to aggregate parallel failures.
- **Concurrency testing** — 100 goroutines + `-race` detector on the IP pool.

## CLI Reference

| Command | Description |
|---|---|
| `orchestrator up -c <config>` | Create network, start DHCP/DNS, containers & VMs |
| `orchestrator down` | Stop and remove all managed resources |
| `orchestrator status` | Show status of network, containers, VMs |
| `orchestrator ssh <vm-name>` | Interactive SSH into a running VM |
| `--log-level debug` | Enable debug logging with goroutine IDs |

## State & Cleanup

- `up` writes `orch-state.json` to the working directory (used by `down` to clean up)
- Temporary DHCP/DNS config directory is removed on `down`

## Links

- **Author**: [Ahmet Türkmen](https://www.linkedin.com/in/mrturkmen/) · [GitHub](https://github.com/mrtrkmn)
- **Talk**: Gophers İstanbul 2026 — *"Orchestrating a Virtual Hub in Go"*
- **[📊 Presentation Slides](https://mrturkmen.com/talks/orchestrator.html)**
- **[📝 Blog Post](https://mrturkmen.com/posts/orchestrating-virtual-hub-in-go/)**

## License

MIT
