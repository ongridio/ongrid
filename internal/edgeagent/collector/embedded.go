package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	gnet "github.com/shirou/gopsutil/v3/net"
	"github.com/shirou/gopsutil/v3/process"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// EmbeddedCollector reads host metrics in-process via gopsutil and emits
// node_exporter-compatible Prometheus metric names. The mapper pairs with
// the same naming convention so the 8-field fast path can be derived
// uniformly from either embedded or scrape output.
//
// The metric subset emitted here is a deliberate carve-out: the five
// resources backing the legacy fast path (cpu, mem, load, net, disk)
// plus a small static set (uname, time). Adding more is one method
// call away — see snapshot().
type EmbeddedCollector struct {
	log    *slog.Logger
	mapper *Mapper

	mu sync.Mutex
}

// NewEmbedded constructs an EmbeddedCollector. log may be nil.
func NewEmbedded(log *slog.Logger) (*EmbeddedCollector, error) {
	if log == nil {
		log = slog.Default()
	}
	return &EmbeddedCollector{
		log:    log,
		mapper: NewMapper(),
	}, nil
}

// CollectAll produces a single CollectorOutput tagged "embedded".
func (c *EmbeddedCollector) CollectAll(ctx context.Context) ([]CollectorOutput, error) {
	now := time.Now()
	families, err := c.snapshot(ctx)
	if err != nil {
		c.log.Warn("collector: embedded snapshot partial",
			slog.Any("err", err),
		)
	}
	if len(families) == 0 {
		return nil, errors.New("collector: embedded snapshot empty")
	}
	hp := c.mapper.MapToHostPoint(now, families)
	samples := FlattenSamples(now, SourceEmbedded, families, nil)
	return []CollectorOutput{{
		Source:         SourceEmbedded,
		HostPoint:      hp,
		HostPointValid: true,
		Samples:        samples,
	}}, nil
}

// HostInfo collects the static host description used by register_edge.
func (c *EmbeddedCollector) HostInfo(ctx context.Context) (tunnel.HostInfo, error) {
	hi := tunnel.HostInfo{
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		CPUCount: runtime.NumCPU(),
	}
	if info, err := host.InfoWithContext(ctx); err == nil && info != nil {
		hi.Hostname = info.Hostname
		hi.KernelVersion = info.KernelVersion
		if info.OS != "" {
			hi.OS = info.OS
		}
		// HostID is gopsutil's per-platform stable identifier:
		//   Linux:   /etc/machine-id (falls back to /var/lib/dbus/machine-id)
		//   macOS:   IOPlatformUUID
		//   Windows: MachineGuid
		// We forward it as the device fingerprint so a re-installed edge
		// agent on the same host keeps mapping to the same device row.
		// NOTE: on cloned Linux VMs HostID collapses to a shared SMBIOS
		// product_uuid — HardwareFingerprint (below) disambiguates those.
		hi.Fingerprint = info.HostID
	}
	// Clone-resistant hardware identity (MAC|CPU|disk). Sent alongside
	// HostID so the cloud prefers it but can still migrate older device
	// rows keyed by HostID. "" when no physical NIC is found.
	hi.HardwareFingerprint = hardwareFingerprint()
	// Primary IPv4 address. Best-effort: "" when no suitable address
	// can be determined (no non-loopback interface with an IPv4 addr).
	hi.IPAddress = primaryIPv4()
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil && vm != nil {
		hi.MemTotalBytes = vm.Total
	}
	if logical, err := cpu.CountsWithContext(ctx, true); err == nil && logical > 0 {
		hi.CPUCount = logical
	}
	return hi, nil
}

// GetHostLoad serves the cloud->edge get_host_load RPC by re-snapshotting
// and running the mapper. Result is the same shape Collect returns but
// in the wire response struct.
func (c *EmbeddedCollector) GetHostLoad(ctx context.Context) (tunnel.GetHostLoadResponse, error) {
	now := time.Now()
	families, _ := c.snapshot(ctx)
	hp := c.mapper.MapToHostPoint(now, families)
	return tunnel.GetHostLoadResponse{
		CPUPct:      hp.CPUPct,
		MemPct:      hp.MemPct,
		DiskUsedPct: hp.DiskUsedPct,
		Load1:       hp.Load1,
		Load5:       hp.Load5,
		Load15:      hp.Load15,
		SampledAt:   now.Unix(),
	}, nil
}

// GetProcessList walks the kernel process table via gopsutil and returns
// topN entries sorted by cpu or mem.
func (c *EmbeddedCollector) GetProcessList(ctx context.Context, topN int, sortBy string) (tunnel.GetProcessListResponse, error) {
	if topN <= 0 {
		topN = 20
	}
	resp := tunnel.GetProcessListResponse{SampledAt: time.Now().Unix()}
	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return resp, fmt.Errorf("processes: %w", err)
	}
	out := make([]tunnel.ProcessInfo, 0, len(procs))
	for _, p := range procs {
		pi := tunnel.ProcessInfo{PID: p.Pid}
		if name, err := p.NameWithContext(ctx); err == nil {
			pi.Name = name
		}
		if cmd, err := p.CmdlineWithContext(ctx); err == nil {
			pi.Cmdline = cmd
		}
		if u, err := p.UsernameWithContext(ctx); err == nil {
			pi.User = u
		}
		if c, err := p.CPUPercentWithContext(ctx); err == nil {
			pi.CPUPct = c
		}
		if m, err := p.MemoryPercentWithContext(ctx); err == nil {
			pi.MemPct = float64(m)
		}
		out = append(out, pi)
	}
	switch sortBy {
	case tunnel.ProcessSortByMem:
		sort.Slice(out, func(i, j int) bool { return out[i].MemPct > out[j].MemPct })
	default:
		sort.Slice(out, func(i, j int) bool { return out[i].CPUPct > out[j].CPUPct })
	}
	if len(out) > topN {
		out = out[:topN]
	}
	resp.Processes = out
	return resp, nil
}

// snapshot reads all configured gopsutil sources and converts them into a
// slice of *dto.MetricFamily mirroring the node_exporter naming convention.
// Errors from individual sub-readers are logged and the family is omitted —
// never propagate a partial-read up.
//
// Metric names emitted (all node_exporter compatible):
//   - node_cpu_seconds_total{cpu, mode}                 counter
//   - node_memory_MemTotal_bytes                        gauge
//   - node_memory_MemAvailable_bytes                    gauge
//   - node_memory_MemFree_bytes                         gauge
//   - node_memory_Buffers_bytes                         gauge
//   - node_memory_Cached_bytes                          gauge
//   - node_load1, node_load5, node_load15               gauge
//   - node_network_receive_bytes_total{device}          counter
//   - node_network_transmit_bytes_total{device}         counter
//   - node_network_receive_packets_total{device}        counter
//   - node_network_transmit_packets_total{device}       counter
//   - node_filesystem_size_bytes{mountpoint, fstype}    gauge
//   - node_filesystem_avail_bytes{mountpoint, fstype}   gauge
//   - node_filesystem_free_bytes{mountpoint, fstype}    gauge
//   - node_time_seconds                                 gauge
//   - node_boot_time_seconds                            gauge
func (c *EmbeddedCollector) snapshot(ctx context.Context) ([]*dto.MetricFamily, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var families []*dto.MetricFamily
	var errs []error

	if mfs, err := c.cpuFamilies(ctx); err == nil {
		families = append(families, mfs...)
	} else {
		errs = append(errs, fmt.Errorf("cpu: %w", err))
	}
	if mfs, err := c.memFamilies(ctx); err == nil {
		families = append(families, mfs...)
	} else {
		errs = append(errs, fmt.Errorf("mem: %w", err))
	}
	if mfs, err := c.loadFamilies(ctx); err == nil {
		families = append(families, mfs...)
	} else {
		errs = append(errs, fmt.Errorf("load: %w", err))
	}
	if mfs, err := c.netFamilies(ctx); err == nil {
		families = append(families, mfs...)
	} else {
		errs = append(errs, fmt.Errorf("net: %w", err))
	}
	if mfs, err := c.fsFamilies(ctx); err == nil {
		families = append(families, mfs...)
	} else {
		errs = append(errs, fmt.Errorf("fs: %w", err))
	}
	families = append(families, c.timeFamilies(ctx)...)
	if len(errs) > 0 {
		return families, errors.Join(errs...)
	}
	return families, nil
}

// --- per-resource builders ----------------------------------------------

func (c *EmbeddedCollector) cpuFamilies(ctx context.Context) ([]*dto.MetricFamily, error) {
	times, err := cpu.TimesWithContext(ctx, true)
	if err != nil {
		return nil, err
	}
	mf := newCounterFamily("node_cpu_seconds_total",
		"Seconds the cpus spent in each mode.")
	for _, t := range times {
		cpuLabel := stripCPUPrefix(t.CPU)
		appendCounter(mf, t.User, label("cpu", cpuLabel), label("mode", "user"))
		appendCounter(mf, t.System, label("cpu", cpuLabel), label("mode", "system"))
		appendCounter(mf, t.Idle, label("cpu", cpuLabel), label("mode", "idle"))
		appendCounter(mf, t.Nice, label("cpu", cpuLabel), label("mode", "nice"))
		appendCounter(mf, t.Iowait, label("cpu", cpuLabel), label("mode", "iowait"))
		appendCounter(mf, t.Irq, label("cpu", cpuLabel), label("mode", "irq"))
		appendCounter(mf, t.Softirq, label("cpu", cpuLabel), label("mode", "softirq"))
		appendCounter(mf, t.Steal, label("cpu", cpuLabel), label("mode", "steal"))
	}
	return []*dto.MetricFamily{mf}, nil
}

func (c *EmbeddedCollector) memFamilies(ctx context.Context) ([]*dto.MetricFamily, error) {
	vm, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return nil, err
	}
	out := []*dto.MetricFamily{
		gaugeFamily("node_memory_MemTotal_bytes", "Memory information field MemTotal_bytes.", float64(vm.Total)),
		gaugeFamily("node_memory_MemAvailable_bytes", "Memory information field MemAvailable_bytes.", float64(vm.Available)),
		gaugeFamily("node_memory_MemFree_bytes", "Memory information field MemFree_bytes.", float64(vm.Free)),
		gaugeFamily("node_memory_Buffers_bytes", "Memory information field Buffers_bytes.", float64(vm.Buffers)),
		gaugeFamily("node_memory_Cached_bytes", "Memory information field Cached_bytes.", float64(vm.Cached)),
	}
	return out, nil
}

func (c *EmbeddedCollector) loadFamilies(ctx context.Context) ([]*dto.MetricFamily, error) {
	avg, err := load.AvgWithContext(ctx)
	if err != nil {
		return nil, err
	}
	return []*dto.MetricFamily{
		gaugeFamily("node_load1", "1m load average.", avg.Load1),
		gaugeFamily("node_load5", "5m load average.", avg.Load5),
		gaugeFamily("node_load15", "15m load average.", avg.Load15),
	}, nil
}

func (c *EmbeddedCollector) netFamilies(ctx context.Context) ([]*dto.MetricFamily, error) {
	stats, err := gnet.IOCountersWithContext(ctx, true)
	if err != nil {
		return nil, err
	}
	rx := newCounterFamily("node_network_receive_bytes_total", "Network device statistic receive_bytes.")
	tx := newCounterFamily("node_network_transmit_bytes_total", "Network device statistic transmit_bytes.")
	rxp := newCounterFamily("node_network_receive_packets_total", "Network device statistic receive_packets.")
	txp := newCounterFamily("node_network_transmit_packets_total", "Network device statistic transmit_packets.")
	for _, s := range stats {
		dev := label("device", s.Name)
		appendCounter(rx, float64(s.BytesRecv), dev)
		appendCounter(tx, float64(s.BytesSent), dev)
		appendCounter(rxp, float64(s.PacketsRecv), dev)
		appendCounter(txp, float64(s.PacketsSent), dev)
	}
	return []*dto.MetricFamily{rx, tx, rxp, txp}, nil
}

func (c *EmbeddedCollector) fsFamilies(ctx context.Context) ([]*dto.MetricFamily, error) {
	parts, err := disk.PartitionsWithContext(ctx, false)
	if err != nil {
		return nil, err
	}
	size := newGaugeFamily("node_filesystem_size_bytes", "Filesystem size in bytes.")
	avail := newGaugeFamily("node_filesystem_avail_bytes", "Filesystem space available to non-root users in bytes.")
	free := newGaugeFamily("node_filesystem_free_bytes", "Filesystem free space in bytes.")
	for _, p := range parts {
		u, err := disk.UsageWithContext(ctx, p.Mountpoint)
		if err != nil || u == nil {
			continue
		}
		labels := []*dto.LabelPair{
			label("mountpoint", p.Mountpoint),
			label("fstype", p.Fstype),
			label("device", p.Device),
		}
		appendGauge(size, float64(u.Total), labels...)
		appendGauge(avail, float64(u.Free), labels...)
		appendGauge(free, float64(u.Free), labels...)
	}
	return []*dto.MetricFamily{size, avail, free}, nil
}

func (c *EmbeddedCollector) timeFamilies(ctx context.Context) []*dto.MetricFamily {
	out := []*dto.MetricFamily{
		gaugeFamily("node_time_seconds", "System time in seconds since epoch (1970).",
			float64(time.Now().Unix())),
	}
	if bt, err := host.BootTimeWithContext(ctx); err == nil {
		out = append(out, gaugeFamily("node_boot_time_seconds", "Node boot time, in unixtime.", float64(bt)))
	}
	return out
}

// --- helpers ------------------------------------------------------------

func stripCPUPrefix(s string) string {
	// gopsutil returns "cpu0", "cpu1", ... — node_exporter labels them
	// just "0", "1". Drop the prefix when present.
	const p = "cpu"
	if len(s) > len(p) && s[:len(p)] == p {
		// some platforms return "cpu0" with numeric tail; verify
		if _, err := strconv.Atoi(s[len(p):]); err == nil {
			return s[len(p):]
		}
	}
	return s
}

func ptrStr(s string) *string { return &s }
func ptrF64(v float64) *float64 {
	return &v
}

func label(name, value string) *dto.LabelPair {
	return &dto.LabelPair{Name: ptrStr(name), Value: ptrStr(value)}
}

func newCounterFamily(name, help string) *dto.MetricFamily {
	t := dto.MetricType_COUNTER
	return &dto.MetricFamily{
		Name: ptrStr(name),
		Help: ptrStr(help),
		Type: &t,
	}
}

func newGaugeFamily(name, help string) *dto.MetricFamily {
	t := dto.MetricType_GAUGE
	return &dto.MetricFamily{
		Name: ptrStr(name),
		Help: ptrStr(help),
		Type: &t,
	}
}

func gaugeFamily(name, help string, v float64) *dto.MetricFamily {
	mf := newGaugeFamily(name, help)
	appendGauge(mf, v)
	return mf
}

func appendCounter(mf *dto.MetricFamily, v float64, labels ...*dto.LabelPair) {
	mf.Metric = append(mf.Metric, &dto.Metric{
		Label:   labels,
		Counter: &dto.Counter{Value: ptrF64(v)},
	})
}

func appendGauge(mf *dto.MetricFamily, v float64, labels ...*dto.LabelPair) {
	mf.Metric = append(mf.Metric, &dto.Metric{
		Label: labels,
		Gauge: &dto.Gauge{Value: ptrF64(v)},
	})
}
