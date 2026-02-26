package ipool

import (
	"net"
	"strings"
	"sync"
	"testing"
)

func TestPool24_RandomReserveRelease(t *testing.T) {
	p, err := NewIPPool("172.18.5.0/24")
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}

	ip, err := p.RandomIP()
	if err != nil {
		t.Fatalf("random ip: %v", err)
	}
	if ip == "" {
		t.Fatalf("empty ip")
	}
	// IP should be in the 172.18.5.x range
	if !strings.HasPrefix(ip, "172.18.5.") {
		t.Fatalf("unexpected ip prefix: %s", ip)
	}
	if err := p.ReleaseIP(ip); err != nil {
		t.Fatalf("release ip: %v", err)
	}
	// Reserve explicit ip
	if err := p.ReserveIP("172.18.5.40"); err != nil {
		t.Fatalf("reserve ip: %v", err)
	}
	if err := p.ReleaseIP("172.18.5.40"); err != nil {
		t.Fatalf("release reserved: %v", err)
	}
}

func TestPool24_FormatIP(t *testing.T) {
	p, err := NewIPPool("172.19.5.0/24")
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	if got := p.FormatIP(1); got != "172.19.5.1" {
		t.Errorf("FormatIP(1) = %s, want 172.19.5.1", got)
	}
	if got := p.FormatIP(254); got != "172.19.5.254" {
		t.Errorf("FormatIP(254) = %s, want 172.19.5.254", got)
	}
}

func TestPool24_BroadcastOffset(t *testing.T) {
	p, err := NewIPPool("172.19.5.0/24")
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	if got := p.BroadcastOffset(); got != 255 {
		t.Errorf("BroadcastOffset = %d, want 255", got)
	}
}

func TestPool16(t *testing.T) {
	p, err := NewIPPool("10.42.0.0/16")
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	if got := p.FormatIP(1); got != "10.42.0.1" {
		t.Errorf("FormatIP(1) = %s, want 10.42.0.1", got)
	}
	if got := p.BroadcastOffset(); got != 65535 {
		t.Errorf("BroadcastOffset = %d, want 65535", got)
	}

	ip, err := p.RandomIP()
	if err != nil {
		t.Fatalf("random ip: %v", err)
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		t.Fatalf("invalid ip: %s", ip)
	}
	// Should be in 10.42.x.x
	if !strings.HasPrefix(ip, "10.42.") {
		t.Fatalf("unexpected ip: %s", ip)
	}

	if err := p.ReserveIP("10.42.1.100"); err != nil {
		t.Fatalf("reserve ip: %v", err)
	}
	// double-reserve should fail
	if err := p.ReserveIP("10.42.1.100"); err == nil {
		t.Fatal("expected error on double reserve")
	}
	if err := p.ReleaseIP("10.42.1.100"); err != nil {
		t.Fatalf("release ip: %v", err)
	}
}

func TestPool8(t *testing.T) {
	p, err := NewIPPool("10.0.0.0/8")
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	if got := p.FormatIP(1); got != "10.0.0.1" {
		t.Errorf("FormatIP(1) = %s, want 10.0.0.1", got)
	}
	if got := p.BroadcastOffset(); got != 16777215 {
		t.Errorf("BroadcastOffset = %d, want 16777215", got)
	}
}

func TestPool12(t *testing.T) {
	p, err := NewIPPool("172.16.0.0/12")
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	if got := p.FormatIP(1); got != "172.16.0.1" {
		t.Errorf("FormatIP(1) = %s, want 172.16.0.1", got)
	}
	if got := p.BroadcastOffset(); got != 1048575 {
		t.Errorf("BroadcastOffset = %d, want 1048575 (2^20 - 1)", got)
	}
}

func TestPool_ReserveOutOfRange(t *testing.T) {
	p, err := NewIPPool("192.168.1.0/24")
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	if err := p.ReserveIP("10.0.0.1"); err == nil {
		t.Fatal("expected error for IP outside subnet")
	}
}

func TestPool_ConcurrentAccess(t *testing.T) {
	p, err := NewIPPool("172.18.5.0/24")
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ip, err := p.RandomIP()
			if err != nil {
				errs <- err
				return
			}
			if err := p.ReleaseIP(ip); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent error: %v", err)
	}
}
