package collector

import (
	"net"
	"strings"
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
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isPhysicalNIC(&c.iface); got != c.want {
				t.Errorf("isPhysicalNIC(%q) = %v, want %v", c.iface.Name, got, c.want)
			}
		})
	}
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

func TestKubernetesNodeFingerprintPrefersUID(t *testing.T) {
	t.Setenv("ONGRID_EDGE_NODE_NAME", "worker-a")
	t.Setenv("ONGRID_EDGE_NODE_UID", "node-uid-1")

	got := kubernetesNodeFingerprint()
	if got == "" || len(got) != 64 {
		t.Fatalf("kubernetesNodeFingerprint() = %q, want sha256 hex", got)
	}

	t.Setenv("ONGRID_EDGE_NODE_UID", "node-uid-2")
	if next := kubernetesNodeFingerprint(); next == got {
		t.Fatalf("node UID change should change fingerprint")
	}
}

func TestKubernetesNodeFingerprintFallsBackToLowercaseNodeName(t *testing.T) {
	t.Setenv("ONGRID_EDGE_NODE_UID", "")
	t.Setenv("ONGRID_EDGE_NODE_NAME", "Worker-A")
	upper := kubernetesNodeFingerprint()

	t.Setenv("ONGRID_EDGE_NODE_NAME", strings.ToLower("Worker-A"))
	lower := kubernetesNodeFingerprint()

	if upper == "" {
		t.Fatal("node name fallback returned empty fingerprint")
	}
	if upper != lower {
		t.Fatalf("node name fallback should be case-insensitive: %q vs %q", upper, lower)
	}
}
