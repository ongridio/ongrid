package collector

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
)

// hardwareFingerprint derives a clone-resistant hardware identity from
// physical-NIC MACs, CPU model, and disk serial numbers. It is ported from
// the liaison-cloud edge agent (pkg/utils/fingerprint.go).
//
// Why not gopsutil HostID: on Linux, HostID prefers the SMBIOS
// /sys/class/dmi/id/product_uuid, which is copied verbatim when a VM is
// cloned — so every clone of one template reports the same HostID and the
// cloud folds them into a single device (issue #96). A hypervisor instead
// hands each clone a fresh NIC MAC, so a MAC-keyed fingerprint distinguishes
// them.
//
// Returns "" when no physical NIC can be found (e.g. exotic netns setups);
// the caller keeps HostID as the fallback fingerprint so such hosts still
// register. All components are sorted so the value is stable across reboots.
func hardwareFingerprint() string {
	macs := physicalMACs()
	if len(macs) == 0 {
		return "" // no usable hardware signal — caller falls back to HostID
	}
	raw := fmt.Sprintf("%s|%s|%s",
		strings.Join(macs, ","),
		cpuSignature(),
		diskSignature(),
	)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// physicalMACs returns up to the first two physical-NIC MAC addresses,
// sorted by interface name, with virtual interfaces filtered out. Empty
// slice when none qualify.
func physicalMACs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	type ni struct{ name, mac string }
	list := make([]ni, 0, len(ifaces))
	for i := range ifaces {
		iface := ifaces[i]
		if !isPhysicalNIC(&iface) {
			continue
		}
		list = append(list, ni{name: iface.Name, mac: iface.HardwareAddr.String()})
	}
	if len(list) == 0 {
		return nil
	}
	sort.Slice(list, func(i, j int) bool { return list[i].name < list[j].name })
	if len(list) > 2 {
		list = list[:2]
	}
	macs := make([]string, len(list))
	for i, n := range list {
		macs[i] = n.mac
	}
	return macs
}

// isPhysicalNIC reports whether iface looks like a real NIC worth keying the
// fingerprint on — excludes loopback, point-to-point (VPN/tunnel), MAC-less,
// and name-matched virtual interfaces (docker/veth/bridge/tun/tap/…).
func isPhysicalNIC(iface *net.Interface) bool {
	if iface.Flags&net.FlagLoopback != 0 {
		return false
	}
	if iface.Flags&net.FlagPointToPoint != 0 {
		return false
	}
	if len(iface.HardwareAddr) == 0 {
		return false
	}
	name := strings.ToLower(iface.Name)
	// Linux uses prefixes; other platforms ship the same virtual NICs under
	// vendor names. One combined keyword list covers both well enough for a
	// fingerprint signal.
	virtual := []string{
		"docker", "veth", "cni", "flannel", "virbr", "br-", "tun", "tap",
		"vmnet", "vboxnet", "vmware", "hyper-v", "vbox", "wsl", "vpn",
		"utun", "bridge", "awdl", "anpi", "isatap", "teredo", "kube",
	}
	for _, v := range virtual {
		if strings.HasPrefix(name, v) || strings.Contains(name, v) {
			return false
		}
	}
	return true
}

// cpuSignature is the deduped+sorted CPU model identity, or "" when gopsutil
// can't read it (it just drops out of the hash — MACs carry the identity).
func cpuSignature() string {
	infos, err := cpu.Info()
	if err != nil || len(infos) == 0 {
		return ""
	}
	seen := make(map[string]struct{}, len(infos))
	uniq := make([]string, 0, len(infos))
	for _, c := range infos {
		s := c.ModelName + c.VendorID + c.Family
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		uniq = append(uniq, s)
	}
	sort.Strings(uniq)
	return strings.Join(uniq, ",")
}

// diskSignature is up to the first two physical-disk serial numbers, sorted
// by device name. "" when no serialled disk is visible.
func diskSignature() string {
	counts, err := disk.IOCounters()
	if err != nil || len(counts) == 0 {
		return ""
	}
	type di struct{ name, serial string }
	list := make([]di, 0, len(counts))
	for _, s := range counts {
		if s.SerialNumber == "" {
			continue
		}
		n := strings.ToLower(s.Name)
		if strings.Contains(n, "virtual") || strings.Contains(n, "loop") {
			continue
		}
		list = append(list, di{name: s.Name, serial: s.SerialNumber})
	}
	if len(list) == 0 {
		return ""
	}
	sort.Slice(list, func(i, j int) bool { return list[i].name < list[j].name })
	if len(list) > 2 {
		list = list[:2]
	}
	serials := make([]string, len(list))
	for i, d := range list {
		serials[i] = d.serial
	}
	return strings.Join(serials, ",")
}

// primaryIPv4 returns the first non-loopback IPv4 address found on the
// host. It prefers addresses on physical NICs (same filter as
// hardwareFingerprint) but falls back to any non-loopback address so
// cloud-only / containerised hosts still get an IP. Returns "" when no
// suitable address is found.
func primaryIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	// First pass: look for an address on a physical NIC.
	for i := range ifaces {
		if !isPhysicalNIC(&ifaces[i]) {
			continue
		}
		addrs, err := ifaces[i].Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ip, _, err := net.ParseCIDR(a.String())
			if err != nil || ip == nil {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil && !ip4.IsLoopback() {
				return ip4.String()
			}
		}
	}
	// Second pass: any non-loopback interface (fallback for cloud VMs
	// where the primary NIC may not match isPhysicalNIC heuristics).
	for i := range ifaces {
		if ifaces[i].Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifaces[i].Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ip, _, err := net.ParseCIDR(a.String())
			if err != nil || ip == nil {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil && !ip4.IsLoopback() {
				return ip4.String()
			}
		}
	}
	return ""
}
