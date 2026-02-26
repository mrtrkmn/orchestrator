package ipool

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"sync"
	"time"
)

// IPPool is a thread-safe pool of assignable host IPs inside an arbitrary
// IPv4 CIDR (supports any prefix length from /8 to /30).
//
// It works with the full range of RFC-1918 private addresses:
//
//	10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16
//
// as well as any sub-range of those (e.g. 10.42.0.0/16, 172.19.5.0/24).
//
// For subnets with more than 1024 host IPs the pool uses a lazy allocation
// strategy (random probe) to avoid pre-materialising millions of map entries.
type IPPool struct {
	subnet string     // original CIDR string
	netIP  net.IP     // network address (4 bytes)
	mask   net.IPMask
	base   uint32     // network address as uint32
	bcast  uint32     // broadcast address as uint32
	start  uint32     // first assignable address
	end    uint32     // last assignable address
	mutex  sync.Mutex
	used   map[uint32]struct{}
	// avail is only populated for small subnets (≤1024 hosts).
	avail map[uint32]struct{}
	lazy  bool // true when using random-probe strategy
	rand  *rand.Rand
}

// lazyThreshold: subnets with host count above this use lazy allocation.
const lazyThreshold = 1024

// NewIPPool creates an IP pool from a CIDR string.
// Assignable IPs exclude:
//   - the network address itself (first)
//   - the broadcast address  (last)
//   - the first 29 host offsets (1-29) which are reserved for infrastructure
//     (gateway, DHCP, DNS, etc.)
func NewIPPool(cidr string) (*IPPool, error) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return nil, errors.New("only IPv4 addresses are supported")
	}

	ones, bits := ipnet.Mask.Size()
	if bits != 32 || ones > 30 {
		return nil, errors.New("only IPv4 with prefix length /8 to /30 is supported")
	}

	netIP := ipnet.IP.To4()
	base := ipToUint32(netIP)
	bcast := base | ^maskToUint32(ipnet.Mask)

	// Assignable range: skip network (base), broadcast (bcast),
	// and the first 29 host offsets reserved for infrastructure.
	start := base + 30
	end := bcast - 1 // last usable host
	if start > end {
		// Very small subnet — fall back to base+2..bcast-1
		start = base + 2
	}

	used := make(map[uint32]struct{})
	hostCount := end - start + 1
	lazy := hostCount > lazyThreshold

	var avail map[uint32]struct{}
	if !lazy {
		avail = make(map[uint32]struct{}, hostCount)
		for addr := start; addr <= end; addr++ {
			avail[addr] = struct{}{}
		}
	}

	src := rand.NewSource(time.Now().UnixNano())
	return &IPPool{
		subnet: cidr,
		netIP:  netIP,
		mask:   ipnet.Mask,
		base:   base,
		bcast:  bcast,
		start:  start,
		end:    end,
		avail:  avail,
		used:   used,
		lazy:   lazy,
		rand:   rand.New(src),
	}, nil
}

// Subnet returns the configured CIDR string.
func (p *IPPool) Subnet() string { return p.subnet }

// FormatIP returns the IP at a given host offset from the network address.
// For example, FormatIP(1) on 172.19.5.0/24 returns "172.19.5.1".
func (p *IPPool) FormatIP(offset int) string {
	return uint32ToIP(p.base + uint32(offset)).String()
}

// RandomIP allocates and returns a random available IP.
func (p *IPPool) RandomIP() (string, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	if p.lazy {
		return p.randomIPLazy()
	}
	return p.randomIPEager()
}

// randomIPEager picks from the pre-materialised avail map (small subnets).
func (p *IPPool) randomIPEager() (string, error) {
	if len(p.avail) == 0 {
		return "", errors.New("no IP available")
	}
	k := p.rand.Intn(len(p.avail))
	i := 0
	var chosen uint32
	for v := range p.avail {
		if i == k {
			chosen = v
			break
		}
		i++
	}
	delete(p.avail, chosen)
	p.used[chosen] = struct{}{}
	return uint32ToIP(chosen).String(), nil
}

// randomIPLazy picks a random address in [start, end] and probes until it finds
// one not in p.used.  Efficient when the pool is large and sparsely allocated.
func (p *IPPool) randomIPLazy() (string, error) {
	total := p.end - p.start + 1
	if uint32(len(p.used)) >= total {
		return "", errors.New("no IP available")
	}

	// Random starting point, then linear probe.
	offset := uint32(p.rand.Int63n(int64(total)))
	for i := uint32(0); i < total; i++ {
		candidate := p.start + (offset+i)%total
		if _, taken := p.used[candidate]; !taken {
			p.used[candidate] = struct{}{}
			return uint32ToIP(candidate).String(), nil
		}
	}
	return "", errors.New("no IP available")
}

// ReserveIP marks an explicit IP as used.
func (p *IPPool) ReserveIP(ip string) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	parsed := net.ParseIP(ip).To4()
	if parsed == nil {
		return errors.New("invalid IPv4 address")
	}
	addr := ipToUint32(parsed)
	if addr <= p.base || addr >= p.bcast {
		return fmt.Errorf("IP %s is outside subnet %s", ip, p.subnet)
	}
	if _, ok := p.used[addr]; ok {
		return errors.New("ip already reserved")
	}
	if !p.lazy {
		delete(p.avail, addr)
	}
	p.used[addr] = struct{}{}
	return nil
}

// ReleaseIP returns a previously reserved IP to the available pool.
func (p *IPPool) ReleaseIP(ip string) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	parsed := net.ParseIP(ip).To4()
	if parsed == nil {
		return errors.New("invalid IPv4 address")
	}
	addr := ipToUint32(parsed)
	if _, ok := p.used[addr]; !ok {
		return errors.New("ip not in used")
	}
	delete(p.used, addr)
	if !p.lazy {
		p.avail[addr] = struct{}{}
	}
	return nil
}

// BroadcastOffset returns the host offset of the broadcast address.
// For a /24 this is 255, for a /16 it is 65535, etc.
func (p *IPPool) BroadcastOffset() int {
	return int(p.bcast - p.base)
}

// ────────────────────────────────────────────────────────────────────────
// helpers
// ────────────────────────────────────────────────────────────────────────

func ipToUint32(ip net.IP) uint32 {
	return binary.BigEndian.Uint32(ip.To4())
}

func uint32ToIP(n uint32) net.IP {
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, n)
	return ip
}

func maskToUint32(m net.IPMask) uint32 {
	return binary.BigEndian.Uint32(m)
}
