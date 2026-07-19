// plugin_linux.go 是 Linux 平台的 hostmetrics plugin 实现。
// subprocess node_exporter 暴露 node_* metrics on :9102（可配置）。
// manager 侧 Prometheus 通过 docker bridge 抓取 host:port。
// 除 subprocess 外，还运行一个 in-process supplementary-metrics producer
// 写 textfile .prom 文件。node_exporter 的 textfile collector 自动读取。
// 首个客户：nf_conntrack — node_exporter 1.8.2 硬编码了错误的 /proc 路径。

//go:build !windows

package hostmetrics

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

// DefaultListenAddress 是 node_exporter 的默认监听地址。
// 9102（非 9100）避免与 manager 容器 metrics 端点冲突。
const DefaultListenAddress = ":9102"

// supplementaryInterval 是 textfile 指标重写间隔。
// 对齐 manager 的 Prom scrape 周期（15s）。
const supplementaryInterval = 15 * time.Second

// New 构造 Linux hostmetrics plugin。binDir 是 node_exporter 二进制所在目录，
// workDir 是 plugin 工作目录。
func New(binDir, workDir string, log *slog.Logger) plugins.Plugin {
	if log == nil {
		log = slog.Default()
	}
	pluginWorkDir := filepath.Join(workDir, Name)
	textfileDir := filepath.Join(pluginWorkDir, "textfile")
	sub := plugins.NewSubprocess(plugins.SubprocessOpts{
		Name:    Name,
		Binary:  filepath.Join(binDir, "node_exporter"),
		WorkDir: pluginWorkDir,
		// node_exporter is CLI-only. ConfigFile 路径存在但不写文件
		//（render 为 nil → 不写）。
		ConfigFile:   filepath.Join(pluginWorkDir, "spec.snapshot"),
		ConfigRender: nil,
		Args: func(cfg plugins.PluginConfig, path string) []string {
			args := buildArgs(cfg, path)
			// 固定 textfile collector 目录，让 supplementary producer 的
			// .prom 文件流入 /metrics。
			args = append(args, "--collector.textfile.directory="+textfileDir)
			return args
		},
		Log: log,
	})
	return &plugin{
		sub:         sub,
		textfileDir: textfileDir,
		log:         log.With(slog.String("plugin", Name)),
	}
}

// plugin 包装 node_exporter subprocess + in-process supplementary-metrics producer。
type plugin struct {
	sub         plugins.Plugin
	textfileDir string
	log         *slog.Logger

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func (p *plugin) Name() string                             { return Name }
func (p *plugin) Configure(cfg plugins.PluginConfig) error { return p.sub.Configure(cfg) }
func (p *plugin) HealthSnapshot() plugins.PluginHealth    { return p.sub.HealthSnapshot() }

// Start 启动 subprocess 和 producer goroutine。producer 用独立 context
// 使其在 Configure-on-the-fly 重启 subprocess 期间存活。
func (p *plugin) Start(ctx context.Context) error {
	if err := os.MkdirAll(p.textfileDir, 0o755); err != nil {
		return fmt.Errorf("hostmetrics: mkdir textfile dir %q: %w", p.textfileDir, err)
	}
	if err := p.sub.Start(ctx); err != nil {
		return err
	}
	p.mu.Lock()
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	pctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.mu.Unlock()
	p.wg.Add(1)
	go p.runSupplementaryProducer(pctx)
	return nil
}

// Stop 先取消 producer + 等待退出，再停 subprocess。
func (p *plugin) Stop(ctx context.Context) error {
	p.mu.Lock()
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.mu.Unlock()
	p.wg.Wait()
	return p.sub.Stop(ctx)
}

// runSupplementaryProducer 每 supplementaryInterval 写 textfile 指标直到 ctx 取消。
// 当前输出：
//   - node_nf_conntrack_entries
//   - node_nf_conntrack_entries_limit
// 原因：node_exporter 1.8.2 的 conntrack collector 硬编码
// /proc/sys/net/nf_conntrack_count，该文件在新内核不存在（值在
// /proc/sys/net/netfilter/nf_conntrack_*）。collector 注册但不输出 —
// manager 的 "conntrack 利用率" 面板空白。直接读 netfilter 路径写 textfile。
func (p *plugin) runSupplementaryProducer(ctx context.Context) {
	defer p.wg.Done()
	tick := time.NewTicker(supplementaryInterval)
	defer tick.Stop()
	p.writeConntrackTextfile()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			p.writeConntrackTextfile()
		}
	}
}

const (
	conntrackCountPath = "/proc/sys/net/netfilter/nf_conntrack_count"
	conntrackMaxPath   = "/proc/sys/net/netfilter/nf_conntrack_max"
	// conntrackLegacyCountPath 是 node_exporter 1.8.2 读取的路径。
	// 如果内核在此路径暴露文件，collector 已在输出，我们必须静默 —
	// 两个源写同一 metric 会导致 Prometheus 拒绝重复 sample。
	conntrackLegacyCountPath = "/proc/sys/net/nf_conntrack_count"
)

func (p *plugin) writeConntrackTextfile() {
	if _, err := os.Stat(conntrackLegacyCountPath); err == nil {
		// 旧内核 — node_exporter 的内置 collector 已处理。
		return
	}
	countBytes, err := os.ReadFile(conntrackCountPath)
	if err != nil {
		// 模块未加载（容器 / 最小化主机）— 静默避免日志刷屏。
		return
	}
	maxBytes, err := os.ReadFile(conntrackMaxPath)
	if err != nil {
		return
	}
	count := strings.TrimSpace(string(countBytes))
	max := strings.TrimSpace(string(maxBytes))
	if count == "" || max == "" {
		return
	}
	body := fmt.Sprintf(`# HELP node_nf_conntrack_entries Number of currently allocated flow entries for connection tracking.
# TYPE node_nf_conntrack_entries gauge
node_nf_conntrack_entries %s
# HELP node_nf_conntrack_entries_limit Maximum size of connection tracking table.
# TYPE node_nf_conntrack_entries_limit gauge
node_nf_conntrack_entries_limit %s
`, count, max)
	target := filepath.Join(p.textfileDir, "conntrack.prom")
	// tmp + rename：node_exporter 不会看到半写文件。
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		p.log.Warn("hostmetrics: write conntrack textfile", slog.Any("err", err))
		return
	}
	if err := os.Rename(tmp, target); err != nil {
		p.log.Warn("hostmetrics: rename conntrack textfile", slog.Any("err", err))
	}
}

func buildArgs(cfg plugins.PluginConfig, _ string) []string {
	listen := stringSpec(cfg, "listen_address", DefaultListenAddress)
	args := []string{
		fmt.Sprintf("--web.listen-address=%s", listen),
	}
	for _, c := range stringSliceSpec(cfg, "collectors_enabled") {
		args = append(args, "--collector."+c)
	}
	for _, c := range stringSliceSpec(cfg, "collectors_disabled") {
		args = append(args, "--no-collector."+c)
	}
	args = append(args, stringSliceSpec(cfg, "extra_args")...)
	return args
}

// stringSpec 从 cfg.Spec 取 string；回退到 def。
func stringSpec(cfg plugins.PluginConfig, key, def string) string {
	if cfg.Spec == nil {
		return def
	}
	if v, ok := cfg.Spec[key].(string); ok && v != "" {
		return v
	}
	return def
}

// stringSliceSpec 从 cfg.Spec 取 []string。JSON unmarshal 为 []interface{}，
// 逐元素 coerce。
func stringSliceSpec(cfg plugins.PluginConfig, key string) []string {
	if cfg.Spec == nil {
		return nil
	}
	raw, ok := cfg.Spec[key].([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
