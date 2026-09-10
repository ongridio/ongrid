package collector

import (
	"net"
	"testing"
)

func TestIsPhysicalNIC(t *testing.T) {
	mac, _ := net.ParseMAC("00:11:22:33:44:55")
	cases := []struct {
		name  string
		iface net.Interface
		want  bool
	}{
		{"physical eth0", net.Interface{Name: "eth0", HardwareAddr: mac}, true},
		{"physical ens33", net.Interface{Name: "ens33", HardwareAddr: mac}, true},
		{"loopback", net.Interface{Name: "lo", Flags: net.FlagLoopback, HardwareAddr: mac}, false},
		{"no mac", net.Interface{Name: "eth0"}, false},
		{"docker bridge", net.Interface{Name: "docker0", HardwareAddr: mac}, false},
		{"veth pair", net.Interface{Name: "veth1234", HardwareAddr: mac}, false},
		{"br- compose", net.Interface{Name: "br-abc123", HardwareAddr: mac}, false},
		{"point-to-point vpn", net.Interface{Name: "tun0", Flags: net.FlagPointToPoint, HardwareAddr: mac}, false},
		{"cni", net.Interface{Name: "cni0", HardwareAddr: mac}, false},
		{"calico host veth", net.Interface{Name: "cali1cd2b48238d", HardwareAddr: mac}, false},
		{"cilium host veth", net.Interface{Name: "lxc1a2b3c4d5", HardwareAddr: mac}, false},
		{"weave", net.Interface{Name: "weave", HardwareAddr: mac}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isPhysicalNIC(&c.iface); got != c.want {
				t.Errorf("isPhysicalNIC(%q) = %v, want %v", c.iface.Name, got, c.want)
			}
		})
	}
}

// Placeholder MACs shared across hosts are rejected regardless of interface
// name — the backstop for CNIs the name keyword list doesn't know yet.
func TestIsPhysicalNICPlaceholderMAC(t *testing.T) {
	for _, macStr := range []string{"ee:ee:ee:ee:ee:ee", "00:00:00:00:00:00"} {
		mac, _ := net.ParseMAC(macStr)
		iface := net.Interface{Name: "eth0", HardwareAddr: mac}
		if isPhysicalNIC(&iface) {
			t.Errorf("isPhysicalNIC(eth0, %s) = true, want false (non-unique placeholder MAC)", macStr)
		}
	}
}

func TestSelectMACs(t *testing.T) {
	mustMAC := func(s string) net.HardwareAddr {
		m, err := net.ParseMAC(s)
		if err != nil {
			t.Fatalf("bad test MAC %q: %v", s, err)
		}
		return m
	}
	real1, real2 := mustMAC("00:50:56:8d:fc:fc"), mustMAC("00:50:56:8d:32:37")
	cali1 := net.Interface{Name: "cali1cd2b48238d", HardwareAddr: mustMAC("ee:ee:ee:ee:ee:ee")}
	cali2 := net.Interface{Name: "calicc4fdafb95b", HardwareAddr: mustMAC("ee:ee:ee:ee:ee:ee")}

	t.Run("calico veths excluded, real NIC kept", func(t *testing.T) {
		// The reported collision: cali* sorts before eth0, so unfiltered the
		// first-two slots were both ee:ee:ee:ee:ee:ee on every node.
		got := selectMACs([]net.Interface{
			cali1, cali2,
			{Name: "eth0", HardwareAddr: real1},
		})
		if len(got) != 1 || got[0] != real1.String() {
			t.Errorf("selectMACs = %v, want [%s]", got, real1)
		}
	})

	t.Run("duplicate macs deduped", func(t *testing.T) {
		// bond0 and its slave eth0 share one address; dedupe frees the
		// second slot for a genuinely different NIC.
		got := selectMACs([]net.Interface{
			{Name: "bond0", HardwareAddr: real1},
			{Name: "eth0", HardwareAddr: real1},
			{Name: "eth1", HardwareAddr: real2},
		})
		if len(got) != 2 || got[0] != real1.String() || got[1] != real2.String() {
			t.Errorf("selectMACs = %v, want [%s %s]", got, real1, real2)
		}
	})

	t.Run("capped at two, sorted by name", func(t *testing.T) {
		got := selectMACs([]net.Interface{
			{Name: "eth2", HardwareAddr: mustMAC("00:00:00:00:00:03")},
			{Name: "eth0", HardwareAddr: mustMAC("00:00:00:00:00:01")},
			{Name: "eth1", HardwareAddr: mustMAC("00:00:00:00:00:02")},
		})
		if len(got) != 2 || got[0] != "00:00:00:00:00:01" || got[1] != "00:00:00:00:00:02" {
			t.Errorf("selectMACs = %v, want [00:00:00:00:00:01 00:00:00:00:00:02]", got)
		}
	})

	t.Run("all filtered returns nil", func(t *testing.T) {
		if got := selectMACs([]net.Interface{cali1, cali2}); got != nil {
			t.Errorf("selectMACs = %v, want nil", got)
		}
	})
}

func TestHardwareFingerprintDeterministic(t *testing.T) {
	// On any host with at least one physical NIC the value is non-empty and
	// stable across calls; on exotic hosts it's "" (the documented fallback
	// signal). Either way two back-to-back calls must agree.
	a := hardwareFingerprint()
	b := hardwareFingerprint()
	if a != b {
		t.Fatalf("hardwareFingerprint not deterministic: %q vs %q", a, b)
	}
	if a != "" && len(a) != 64 {
		t.Fatalf("non-empty fingerprint must be a sha256 hex (64 chars), got %d", len(a))
	}
}
