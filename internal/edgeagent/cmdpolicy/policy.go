//go:build linux

package cmdpolicy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// policy.go is where the v1 read-only baseline lives. Two entry points:
//
//   - DefaultReadOnly() returns the curated baseline.
//   - LoadFromYAML(path, base) merges a yaml override on top of base.
//
// The merge semantics (documented on LoadFromYAML) are deliberately
// "additive with override": the operator file can extend the binary
// list, replace an existing binary's policy, narrow the path
// allowlist, etc. There is no "remove this binary" verb in v1 — to
// remove a binary the operator restarts with `base = empty`. We can
// add a remove verb later without breaking compat.

const (
	defaultStdoutCap = 64 * 1024
	defaultStderrCap = 16 * 1024
	defaultTimeout   = 5 * time.Minute
	defaultMaxArgs   = 32
)

// DefaultReadOnly returns the production-default read-only policy. The
// binary list is intentionally curated — see comments below for class
// counts. Operators extend via LoadFromYAML.
func DefaultReadOnly() *Policy {
	p := &Policy{
		bins:      map[string]*BinaryPolicy{},
		StdoutCap: defaultStdoutCap,
		StderrCap: defaultStderrCap,
		Timeout:   defaultTimeout,
		MaxArgs:   defaultMaxArgs,
		PathAllowlist: []string{
			"/var", "/opt", "/home", "/tmp", "/srv", "/data",
		},
		// Empty NetworkHostAllowlist means deny ALL outbound. Operators
		// who want their LLM to ping internal services add CIDRs here.
		NetworkHostAllowlist: nil,
	}

	// ----- ClassReadFS (16) -----
	for _, b := range []string{
		"cat", "head", "tail", "tac", "less", "ls", "find",
		"du", "stat", "readlink", "file", "tree", "wc",
		"grep", "egrep", "fgrep",
	} {
		p.addBin(&BinaryPolicy{Bin: b, Class: ClassReadFS})
	}
	// find: kill -delete / -exec branches and side-effecting prints.
	p.addBin(&BinaryPolicy{
		Bin:        "find",
		Class:      ClassReadFS,
		DeniedArgs: []string{"-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprintf", "-fls"},
	})
	p.addBin(&BinaryPolicy{
		Bin:        "awk",
		Class:      ClassReadFS,
		DeniedArgs: []string{"system(", "| sh", "| bash", "exec("},
	})
	p.addBin(&BinaryPolicy{
		Bin:        "sed",
		Class:      ClassReadFS,
		DeniedArgs: []string{"-i", "--in-place"},
	})

	// ----- ClassReadSystem (17) -----
	for _, b := range []string{
		"ps", "top", "uptime", "free", "df", "iostat", "vmstat",
		"mpstat", "pidstat", "lsof", "ss", "netstat", "dmesg",
		"who", "w", "uname", "id", "groups",
	} {
		p.addBin(&BinaryPolicy{Bin: b, Class: ClassReadSystem})
	}
	p.addBin(&BinaryPolicy{
		Bin:        "hostname",
		Class:      ClassReadSystem,
		DeniedArgs: []string{"-b", "-s", "--set"},
	})
	p.addBin(&BinaryPolicy{
		Bin:        "date",
		Class:      ClassReadSystem,
		DeniedArgs: []string{"-s", "--set"},
	})
	p.addBin(&BinaryPolicy{
		Bin:   "journalctl",
		Class: ClassReadSystem,
		DeniedArgs: []string{
			"--rotate", "--vacuum-time", "--vacuum-size", "--vacuum-files",
			"--flush", "--sync", "--relinquish-var", "--smart-relinquish-var",
		},
	})

	// ----- ClassMixed (7) -----
	// iptables: read = -L/-S/-C/-n; write = -A/-I/-D/-R/-F/-X/-N/-P/-Z.
	p.addBin(&BinaryPolicy{
		Bin:   "iptables",
		Class: ClassMixed,
		ReadOnlyMatchers: []ArgMatcher{
			{AnyFlag: []string{"-L", "--list", "-S", "--list-rules", "-C", "--check", "-n", "--numeric"}},
		},
		WriteMatchers: []ArgMatcher{
			{AnyFlag: []string{"-A", "-I", "-D", "-R", "-F", "--flush", "-X", "-N", "-P", "-Z"}},
		},
	})
	p.addBin(&BinaryPolicy{
		Bin:   "ip6tables",
		Class: ClassMixed,
		ReadOnlyMatchers: []ArgMatcher{
			{AnyFlag: []string{"-L", "--list", "-S", "--list-rules", "-C", "--check", "-n", "--numeric"}},
		},
		WriteMatchers: []ArgMatcher{
			{AnyFlag: []string{"-A", "-I", "-D", "-R", "-F", "--flush", "-X", "-N", "-P", "-Z"}},
		},
	})
	// tc: read = qdisc/class/filter show; write = add/del/replace/...
	p.addBin(&BinaryPolicy{
		Bin:   "tc",
		Class: ClassMixed,
		ReadOnlyMatchers: []ArgMatcher{
			{SubcmdPath: []string{"qdisc", "show"}},
			{SubcmdPath: []string{"class", "show"}},
			{SubcmdPath: []string{"filter", "show"}},
			{Subcmd: "-s"},
		},
		WriteMatchers: []ArgMatcher{
			{AnyFlag: []string{"add", "del", "replace", "change", "link"}},
		},
	})
	// systemctl: read = status / show / cat / list-* / is-*; write = start/stop/etc.
	p.addBin(&BinaryPolicy{
		Bin:   "systemctl",
		Class: ClassMixed,
		ReadOnlyMatchers: []ArgMatcher{
			{AnyFlag: []string{
				"status", "show", "cat", "list-units", "list-jobs", "list-sockets",
				"list-timers", "list-dependencies", "is-active", "is-enabled",
				"is-failed", "get-default", "show-environment",
			}},
		},
		WriteMatchers: []ArgMatcher{
			{AnyFlag: []string{
				"start", "stop", "restart", "reload", "try-restart",
				"enable", "disable", "mask", "unmask", "kill",
				"reboot", "poweroff", "halt", "kexec", "rescue", "emergency",
				"set-default", "set-property", "import-environment",
				"unset-environment", "reset-failed", "daemon-reload", "edit",
			}},
		},
	})
	// ip: read = addr/link/route/neigh/rule/netns show / list; write =
	// add/del/set/exec. `netns exec` is a recursive shell — it would let
	// the LLM bypass cmdpolicy by re-entering with arbitrary argv inside
	// the namespace — so it MUST land in WriteMatchers (denied without
	// reviewer). Per-namespace read inspection still works via `ip -n
	// <ns> addr show / route show / link show` because the `-n <ns>`
	// flag is a global option not a subcmd; it precedes the read subcmds
	// already covered above.
	p.addBin(&BinaryPolicy{
		Bin:   "ip",
		Class: ClassMixed,
		ReadOnlyMatchers: []ArgMatcher{
			{SubcmdPath: []string{"addr", "show"}},
			{SubcmdPath: []string{"link", "show"}},
			{SubcmdPath: []string{"route", "show"}},
			{SubcmdPath: []string{"neigh", "show"}},
			{SubcmdPath: []string{"rule", "show"}},
			{SubcmdPath: []string{"netns", "list"}},
			{SubcmdPath: []string{"netns", "identify"}},
			{SubcmdPath: []string{"netns", "pids"}},
			{Subcmd: "-s"},
			// Bare "ip addr" / "ip link" / "ip route" / "ip netns" with
			// no subcmd defaults to show / list in iproute2 — accept as read.
			{Subcmd: "addr"},
			{Subcmd: "link"},
			{Subcmd: "route"},
			{Subcmd: "neigh"},
			{Subcmd: "rule"},
			{Subcmd: "netns"},
			{Subcmd: "tunnel"},
			{Subcmd: "monitor"},
		},
		WriteMatchers: []ArgMatcher{
			{AnyFlag: []string{"add", "del", "set", "replace", "change", "flush"}},
			// `ip netns exec <ns> <cmd>` re-enters the cmdpolicy boundary
			// with an arbitrary command — refuse outright.
			{SubcmdPath: []string{"netns", "exec"}},
			{SubcmdPath: []string{"netns", "add"}},
			{SubcmdPath: []string{"netns", "del"}},
		},
	})
	// mount: -l/--list/-t = read; raw `mount /a /b` (≥2 absolute paths)
	// is mount-this-on-that = write. We encode the absolute-path heuristic
	// in Sandbox.Decide (see classifyMount) since matchers can't express
	// "two of the args start with /".
	p.addBin(&BinaryPolicy{
		Bin:   "mount",
		Class: ClassMixed,
		ReadOnlyMatchers: []ArgMatcher{
			{AnyFlag: []string{"-l", "--list", "-t", "--types"}},
			// Bare `mount` with no args is the read-only "show all" form.
			{}, // catch-all read fallback used only when no write match
		},
		WriteMatchers: nil, // see classifyMount
	})
	// crontab: -l/--list = read; -e/-r/-i = write.
	p.addBin(&BinaryPolicy{
		Bin:   "crontab",
		Class: ClassMixed,
		ReadOnlyMatchers: []ArgMatcher{
			{AnyFlag: []string{"-l", "--list"}},
		},
		WriteMatchers: []ArgMatcher{
			{AnyFlag: []string{"-e", "-r", "-i"}},
		},
	})

	// ----- Advanced network probes (Layer 1 of network-research support):
	// OVS / nftables / conntrack / eBPF read paths so the LLM can drive
	// host_bash for ovs-vsctl show / ovs-ofctl dump-flows / nft list ruleset
	// / conntrack -L / bpftool prog show / bpftool map dump etc.
	//
	// Class is Mixed across the board — each tool has obvious read vs write
	// separation. eBPF write side (load / attach) is denied at this layer;
	// LLM-driven kernel-program load is too dangerous for a generic chat
	// path. bpftrace / perf record stay outside this allowlist entirely
	// and will be exposed only via the Layer-3 preset library when that
	// lands.
	//
	// ovs-vsctl: read = list-* / show / get / find / list; write = add-* / del-* / set / remove / clear / destroy.
	p.addBin(&BinaryPolicy{
		Bin:   "ovs-vsctl",
		Class: ClassMixed,
		ReadOnlyMatchers: []ArgMatcher{
			{AnyFlag: []string{
				"list-br", "list-ports", "list-ifaces", "list-managers",
				"list-controller", "list-cmds", "show", "get", "find", "list",
				"br-exists", "iface-to-br", "port-to-br",
			}},
		},
		WriteMatchers: []ArgMatcher{
			{AnyFlag: []string{
				"add-br", "del-br", "add-port", "del-port",
				"add-bond", "set", "remove", "clear", "destroy",
				"set-controller", "del-controller", "set-fail-mode", "del-fail-mode",
				"set-manager", "del-manager", "init",
			}},
		},
	})
	// ovs-ofctl: OpenFlow tool. Read dumps + show + monitor; write add-/mod-/del-flow.
	p.addBin(&BinaryPolicy{
		Bin:   "ovs-ofctl",
		Class: ClassMixed,
		ReadOnlyMatchers: []ArgMatcher{
			{AnyFlag: []string{
				"show", "dump-flows", "dump-aggregate", "dump-tables", "dump-ports",
				"dump-ports-desc", "dump-table-features", "dump-groups",
				"dump-group-stats", "dump-group-features", "dump-meters",
				"dump-meter-stats", "dump-tlv-map", "queue-stats", "queue-get-config",
				"monitor", "snoop", "get-frags", "probe", "ping",
			}},
		},
		WriteMatchers: []ArgMatcher{
			{AnyFlag: []string{
				"add-flow", "add-flows", "mod-flows", "del-flows", "replace-flows",
				"add-group", "mod-group", "del-groups", "insert-buckets",
				"remove-buckets", "add-meter", "mod-meter", "del-meters",
				"set-frags", "mod-port", "mod-table",
			}},
		},
	})
	// ovs-dpctl: datapath. Read = show / dump-flows; write = add-dp / del-dp / add-flow.
	p.addBin(&BinaryPolicy{
		Bin:   "ovs-dpctl",
		Class: ClassMixed,
		ReadOnlyMatchers: []ArgMatcher{
			{AnyFlag: []string{"show", "dump-flows", "dump-conntrack", "dump-dps", "ct-bkts"}},
		},
		WriteMatchers: []ArgMatcher{
			{AnyFlag: []string{"add-dp", "del-dp", "add-if", "del-if", "add-flow", "del-flow", "del-flows", "set-if", "flush-conntrack"}},
		},
	})
	// ovs-appctl: runtime control. Mostly read (fdb/show, lacp/show, ofproto/trace etc).
	// Few writers (vlog/set, fdb/flush). Default class read; explicit writers in WriteMatchers.
	p.addBin(&BinaryPolicy{
		Bin:   "ovs-appctl",
		Class: ClassMixed,
		ReadOnlyMatchers: []ArgMatcher{
			// ovs-appctl uses slash-paths: "fdb/show", "lacp/show", "ofproto/trace".
			// We accept any first arg that is NOT in the deny list. Done by the
			// catch-all read entry; specific writers below override.
			{}, // catch-all read
		},
		WriteMatchers: []ArgMatcher{
			{AnyFlag: []string{
				"vlog/set", "fdb/flush", "fdb/del", "lacp/show-stats-clear",
				"bond/set-active-slave", "bridge/destroy", "exit", "stop",
			}},
		},
	})
	// nft: read = "list ..."; write = add/delete/flush/create/insert/replace.
	// nft uses a subcommand grammar (no flags) so SubcmdPath fits.
	p.addBin(&BinaryPolicy{
		Bin:   "nft",
		Class: ClassMixed,
		ReadOnlyMatchers: []ArgMatcher{
			{Subcmd: "list"},
			{Subcmd: "describe"},
			{Subcmd: "monitor"},
		},
		WriteMatchers: []ArgMatcher{
			{AnyFlag: []string{"add", "delete", "flush", "create", "insert", "replace", "rename", "reset"}},
		},
	})
	// conntrack: -L (list) / -S (stats) / -G (get) / -E (event stream) read;
	// -D (delete) / -F (flush) / -I (create) / -U (update) write.
	p.addBin(&BinaryPolicy{
		Bin:   "conntrack",
		Class: ClassMixed,
		ReadOnlyMatchers: []ArgMatcher{
			{AnyFlag: []string{"-L", "--dump", "-S", "--stats", "-G", "--get", "-E", "--event", "-C", "--count"}},
		},
		WriteMatchers: []ArgMatcher{
			{AnyFlag: []string{"-D", "--delete", "-F", "--flush", "-I", "--create", "-U", "--update"}},
		},
	})
	// ipset: list (read); create/add/del/flush/destroy (write).
	p.addBin(&BinaryPolicy{
		Bin:   "ipset",
		Class: ClassMixed,
		ReadOnlyMatchers: []ArgMatcher{
			{AnyFlag: []string{"list", "save", "test", "version", "help"}},
		},
		WriteMatchers: []ArgMatcher{
			{AnyFlag: []string{"create", "add", "del", "flush", "destroy", "rename", "swap", "restore"}},
		},
	})
	// ethtool: read by default (-i / -S / -k / -c / -g / -l / -m / -P etc);
	// write = -A / -C / -G / -K / -L / -s / -X / --reset etc. ethtool args
	// are single-letter flags with optional second-arg subcommands; the
	// matcher pattern works as long as we list the "writer" letters in the
	// deny set.
	p.addBin(&BinaryPolicy{
		Bin:   "ethtool",
		Class: ClassMixed,
		ReadOnlyMatchers: []ArgMatcher{
			// Most ethtool invocations are `ethtool <iface>` (no flag) →
			// dump driver info. Accept catch-all read; writers below
			// override before this rule fires.
			{},
		},
		WriteMatchers: []ArgMatcher{
			{AnyFlag: []string{
				"-A", "--pause", "-C", "--coalesce", "-G", "--set-ring",
				"-K", "--features", "--offload", "-L", "--set-channels",
				"-X", "--set-rxfh-indir", "-s", "--change", "-r", "--negotiate",
				"-N", "--config-nfc", "-U", "--config-ntuple",
				"--reset", "--set-priv-flags", "--set-eee",
			}},
		},
	})
	// bpftool: read = prog show/list, map dump/show, btf dump, net show,
	// link show, iter list, version, feature probe (read-only kernel cap probe).
	// write = prog load/attach/detach/pin, map create/update/delete/pin, link create.
	p.addBin(&BinaryPolicy{
		Bin:   "bpftool",
		Class: ClassMixed,
		ReadOnlyMatchers: []ArgMatcher{
			{SubcmdPath: []string{"prog", "show"}},
			{SubcmdPath: []string{"prog", "list"}},
			{SubcmdPath: []string{"prog", "dump"}},
			{SubcmdPath: []string{"map", "show"}},
			{SubcmdPath: []string{"map", "list"}},
			{SubcmdPath: []string{"map", "dump"}},
			{SubcmdPath: []string{"map", "lookup"}},
			{SubcmdPath: []string{"btf", "show"}},
			{SubcmdPath: []string{"btf", "list"}},
			{SubcmdPath: []string{"btf", "dump"}},
			{SubcmdPath: []string{"net", "show"}},
			{SubcmdPath: []string{"net", "list"}},
			{SubcmdPath: []string{"link", "show"}},
			{SubcmdPath: []string{"link", "list"}},
			{SubcmdPath: []string{"iter", "list"}},
			{SubcmdPath: []string{"feature", "probe"}},
			{SubcmdPath: []string{"perf", "show"}},
			{SubcmdPath: []string{"perf", "list"}},
			{Subcmd: "version"},
			{Subcmd: "help"},
			// Bare `bpftool prog` / `map` / `btf` / `net` defaults to show.
			{Subcmd: "prog"},
			{Subcmd: "map"},
			{Subcmd: "btf"},
			{Subcmd: "net"},
			{Subcmd: "link"},
			{Subcmd: "iter"},
			{Subcmd: "feature"},
		},
		WriteMatchers: []ArgMatcher{
			{SubcmdPath: []string{"prog", "load"}},
			{SubcmdPath: []string{"prog", "loadall"}},
			{SubcmdPath: []string{"prog", "attach"}},
			{SubcmdPath: []string{"prog", "detach"}},
			{SubcmdPath: []string{"prog", "pin"}},
			{SubcmdPath: []string{"prog", "unpin"}},
			{SubcmdPath: []string{"map", "create"}},
			{SubcmdPath: []string{"map", "update"}},
			{SubcmdPath: []string{"map", "delete"}},
			{SubcmdPath: []string{"map", "pin"}},
			{SubcmdPath: []string{"map", "unpin"}},
			{SubcmdPath: []string{"link", "create"}},
			{SubcmdPath: []string{"link", "detach"}},
			{SubcmdPath: []string{"link", "pin"}},
			{SubcmdPath: []string{"cgroup", "attach"}},
			{SubcmdPath: []string{"cgroup", "detach"}},
		},
	})
	// ----- ClassNetwork (7) -----
	// nc: -z (port probe) only; -e/-c/-l (listen / exec) denied.
	p.addBin(&BinaryPolicy{
		Bin:   "nc",
		Class: ClassNetwork,
		ReadOnlyMatchers: []ArgMatcher{
			{AnyFlag: []string{"-z"}},
		},
		DeniedArgs: []string{"-e", "-c", "-l"},
	})
	p.addBin(&BinaryPolicy{
		Bin:   "curl",
		Class: ClassNetwork,
		ReadOnlyMatchers: []ArgMatcher{
			{AnyFlag: []string{"--head", "-I"}},
		},
		DeniedArgs: []string{"-o", "-O", "--output", "-T", "--upload-file"},
	})
	p.addBin(&BinaryPolicy{
		Bin:   "wget",
		Class: ClassNetwork,
		ReadOnlyMatchers: []ArgMatcher{
			{AnyFlag: []string{"--spider"}},
		},
		DeniedArgs: []string{"-O", "-o"},
	})
	p.addBin(&BinaryPolicy{Bin: "dig", Class: ClassNetwork})
	p.addBin(&BinaryPolicy{Bin: "host", Class: ClassNetwork})
	p.addBin(&BinaryPolicy{Bin: "nslookup", Class: ClassNetwork})
	p.addBin(&BinaryPolicy{
		Bin:        "ping",
		Class:      ClassNetwork,
		DeniedArgs: []string{"-f"},
	})
	p.addBin(&BinaryPolicy{Bin: "traceroute", Class: ClassNetwork})

	// ----- ClassDenied (~25) -----
	for _, b := range []string{
		// shells
		"bash", "sh", "zsh", "dash", "ash",
		// scripting
		"python", "python3", "perl", "ruby", "node", "lua", "tcl",
		// destructive fs
		"rm", "rmdir", "mv", "cp", "dd", "mkfs", "truncate", "shred",
		// permission change
		"chmod", "chown", "chgrp", "setfacl",
		// system mutating
		"shutdown", "reboot", "halt", "poweroff", "kexec",
		// auth
		"useradd", "userdel", "usermod", "groupadd", "groupdel",
		"passwd", "chpasswd",
		// rule-table replace
		"iptables-restore", "ip6tables-restore",
	} {
		p.addBin(&BinaryPolicy{Bin: b, Class: ClassDenied})
	}

	// Resolve binary abs paths once at construction. Missing binaries
	// keep AbsPath="" — Decide() reports "not installed" cleanly.
	for _, bp := range p.bins {
		if bp.Class == ClassDenied {
			continue
		}
		bp.AbsPath = discoverBin(bp.Bin)
	}
	return p
}

// addBin registers/overrides a binary policy. When called multiple
// times with the same Bin (e.g. find: first the simple registration,
// then a richer one with DeniedArgs) the LATER call wins so the loop +
// override pattern in DefaultReadOnly works as written.
func (p *Policy) addBin(bp *BinaryPolicy) {
	if p.bins == nil {
		p.bins = map[string]*BinaryPolicy{}
	}
	p.bins[bp.Bin] = bp
}

// Lookup returns the per-binary policy by basename (no path component),
// or nil when the binary is not in the policy at all (treated as
// "denied — unknown binary" by the caller).
func (p *Policy) Lookup(bin string) *BinaryPolicy {
	if p == nil {
		return nil
	}
	return p.bins[filepath.Base(bin)]
}

// Bins returns the registered binary basenames (sorted lexicographically
// is the caller's job; the policy doesn't promise iteration order).
// Used by handler boot logging + tests.
func (p *Policy) Bins() []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.bins))
	for k := range p.bins {
		out = append(out, k)
	}
	return out
}

// discoverBin resolves a binary basename to an absolute path. Same
// pattern as host_files: try LookPath, fall back to the conventional
// /usr/sbin → /sbin → /usr/local/bin chain. Returns "" when not found
// anywhere.
func discoverBin(name string) string {
	if abs, err := exec.LookPath(name); err == nil {
		return abs
	}
	for _, prefix := range []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin", "/usr/local/bin"} {
		candidate := filepath.Join(prefix, name)
		if _, err := exec.LookPath(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// =====================================================================
// YAML override
// =====================================================================

// yamlPolicy is the wire shape of /etc/ongrid-edge/bash-policy.yaml.
// The shape is intentionally narrow — operators express what they want
// to add / replace; everything else is taken from base.
type yamlPolicy struct {
	Binaries []struct {
		Name             string        `yaml:"name"`
		Class            string        `yaml:"class"`
		ReadOnlyMatchers []yamlMatcher `yaml:"read_only_matchers"`
		WriteMatchers    []yamlMatcher `yaml:"write_matchers"`
		DeniedArgs       []string      `yaml:"denied_args"`
	} `yaml:"binaries"`
	NetworkHostAllowlist []string `yaml:"network_host_allowlist"`
	StdoutCapBytes       int      `yaml:"stdout_cap_bytes"`
	StderrCapBytes       int      `yaml:"stderr_cap_bytes"`
	TimeoutSeconds       int      `yaml:"timeout_seconds"`
	MaxArgvLength        int      `yaml:"max_argv_length"`
	PathAllowlist        []string `yaml:"path_allowlist"`
}

type yamlMatcher struct {
	AnyFlag    []string `yaml:"any_flag"`
	Subcmd     string   `yaml:"subcmd"`
	SubcmdPath []string `yaml:"subcmd_path"`
}

// LoadFromYAML reads `path` and merges it on top of `base`. Merge
// semantics:
//
//   - binaries[*]: each entry FULLY REPLACES the same-name entry in
//     base (no per-field merge — the YAML is the new source of truth
//     for that binary). New names are appended.
//   - network_host_allowlist / path_allowlist: REPLACE when set,
//     keep base's when absent.
//   - caps / timeout / max_argv_length: REPLACE when > 0, keep
//     base's when 0 / absent.
//
// `base` may be nil; the result is then a freshly-initialised Policy
// with only the YAML-provided rules. Pass DefaultReadOnly() to start
// from the curated baseline.
//
// On parse error / file-not-found the returned error includes the path
// so the operator sees what we tried to load.
func LoadFromYAML(path string, base *Policy) (*Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cmdpolicy: read %q: %w", path, err)
	}
	var raw yamlPolicy
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("cmdpolicy: parse %q: %w", path, err)
	}
	out := clonePolicy(base)
	for _, b := range raw.Binaries {
		if strings.TrimSpace(b.Name) == "" {
			return nil, fmt.Errorf("cmdpolicy: %q: binary entry with empty name", path)
		}
		class := BinaryClass(strings.TrimSpace(b.Class))
		if !validClass(class) {
			return nil, fmt.Errorf("cmdpolicy: %q: binary %q has invalid class %q", path, b.Name, b.Class)
		}
		bp := &BinaryPolicy{
			Bin:              b.Name,
			Class:            class,
			ReadOnlyMatchers: convertMatchers(b.ReadOnlyMatchers),
			WriteMatchers:    convertMatchers(b.WriteMatchers),
			DeniedArgs:       append([]string(nil), b.DeniedArgs...),
		}
		if class != ClassDenied {
			bp.AbsPath = discoverBin(bp.Bin)
		}
		out.addBin(bp)
	}
	if raw.NetworkHostAllowlist != nil {
		out.NetworkHostAllowlist = append([]string(nil), raw.NetworkHostAllowlist...)
	}
	if raw.PathAllowlist != nil {
		out.PathAllowlist = append([]string(nil), raw.PathAllowlist...)
	}
	if raw.StdoutCapBytes > 0 {
		out.StdoutCap = raw.StdoutCapBytes
	}
	if raw.StderrCapBytes > 0 {
		out.StderrCap = raw.StderrCapBytes
	}
	if raw.TimeoutSeconds > 0 {
		out.Timeout = time.Duration(raw.TimeoutSeconds) * time.Second
	}
	if raw.MaxArgvLength > 0 {
		out.MaxArgs = raw.MaxArgvLength
	}
	return out, nil
}

func validClass(c BinaryClass) bool {
	switch c {
	case ClassReadFS, ClassReadSystem, ClassMixed, ClassNetwork, ClassDenied:
		return true
	}
	return false
}

func convertMatchers(in []yamlMatcher) []ArgMatcher {
	if len(in) == 0 {
		return nil
	}
	out := make([]ArgMatcher, 0, len(in))
	for _, m := range in {
		out = append(out, ArgMatcher{
			AnyFlag:    append([]string(nil), m.AnyFlag...),
			Subcmd:     m.Subcmd,
			SubcmdPath: append([]string(nil), m.SubcmdPath...),
		})
	}
	return out
}

// clonePolicy deep-copies a Policy so YAML overrides don't mutate the
// caller's base. Nil base produces a freshly-initialised empty Policy
// (caller likely wants DefaultReadOnly + LoadFromYAML, not "from
// scratch", but we support both).
func clonePolicy(base *Policy) *Policy {
	if base == nil {
		return &Policy{
			bins:      map[string]*BinaryPolicy{},
			StdoutCap: defaultStdoutCap,
			StderrCap: defaultStderrCap,
			Timeout:   defaultTimeout,
			MaxArgs:   defaultMaxArgs,
		}
	}
	out := &Policy{
		bins:                 make(map[string]*BinaryPolicy, len(base.bins)),
		NetworkHostAllowlist: append([]string(nil), base.NetworkHostAllowlist...),
		StdoutCap:            base.StdoutCap,
		StderrCap:            base.StderrCap,
		Timeout:              base.Timeout,
		MaxArgs:              base.MaxArgs,
		PathAllowlist:        append([]string(nil), base.PathAllowlist...),
	}
	for k, v := range base.bins {
		bp := *v
		bp.ReadOnlyMatchers = append([]ArgMatcher(nil), v.ReadOnlyMatchers...)
		bp.WriteMatchers = append([]ArgMatcher(nil), v.WriteMatchers...)
		bp.DeniedArgs = append([]string(nil), v.DeniedArgs...)
		out.bins[k] = &bp
	}
	return out
}

// =====================================================================
// Decide (no path / network checks here — the Sandbox layer adds those)
// =====================================================================

// Decide checks `cmd` (which may contain pipes) against the policy.
// Path validation and network host validation are NOT performed here —
// the Sandbox layer adds those, because they need a PathValidator
// dependency. Use Sandbox.Decide for the full check.
func (p *Policy) Decide(cmd string) Decision {
	segments, err := SplitPipes(cmd)
	if err != nil {
		return Decision{Allow: false, Reason: err.Error()}
	}
	for i, seg := range segments {
		if len(seg) > p.MaxArgs {
			return Decision{
				Allow:    false,
				Reason:   fmt.Sprintf("segment %d has %d args (max %d)", i, len(seg), p.MaxArgs),
				Segments: segments,
			}
		}
		if d := p.decideSegment(seg); !d.Allow {
			d.Segments = segments
			return d
		}
	}
	return Decision{Allow: true, Segments: segments}
}

// decideSegment is the per-pipe-segment classification. argv[0] is the
// binary, argv[1:] are the args. Returns a Decision; only Allow +
// Reason are populated (Segments is set by the caller).
func (p *Policy) decideSegment(argv []string) Decision {
	if len(argv) == 0 {
		return Decision{Allow: false, Reason: "empty argv segment"}
	}
	bin := filepath.Base(argv[0])
	bp := p.bins[bin]
	if bp == nil {
		return Decision{Allow: false, Reason: fmt.Sprintf("binary %q is not in the policy", bin)}
	}
	if bp.Class == ClassDenied {
		return Decision{Allow: false, Reason: fmt.Sprintf("binary %q is in denied class", bin)}
	}
	if bp.AbsPath == "" {
		return Decision{Allow: false, Reason: fmt.Sprintf("binary %q is not installed on this host", bin)}
	}
	rest := argv[1:]
	// 1. Global denied-args check (substring match — catches "system("
	// embedded in awk programs as well as bare flags like "-i").
	for _, denied := range bp.DeniedArgs {
		for _, a := range rest {
			if strings.Contains(a, denied) {
				return Decision{
					Allow:  false,
					Reason: fmt.Sprintf("binary %q: arg %q matches denied token %q", bin, a, denied),
				}
			}
		}
	}
	// 2. Class-specific decision.
	switch bp.Class {
	case ClassReadFS, ClassReadSystem:
		return Decision{Allow: true}
	case ClassMixed:
		// Special heuristic: mount with ≥2 absolute path args =
		// mounting one onto the other = write. Encode here because
		// matchers can't express "≥2 of the args start with /".
		if bin == "mount" && countAbsolutePaths(rest) >= 2 {
			return Decision{Allow: false, Reason: "mount: refusing mount-this-on-that form (write)"}
		}
		// kill heuristic: no -l/-L means the call sends a signal = write.
		if bin == "kill" {
			if hasAnyFlag(rest, []string{"-l", "-L"}) {
				return Decision{Allow: true}
			}
			return Decision{Allow: false, Reason: "kill: sending a signal is a write operation"}
		}
		// Any WriteMatcher hit = REJECT.
		if matcherListMatches(rest, bp.WriteMatchers) {
			return Decision{Allow: false, Reason: fmt.Sprintf("binary %q: argv matches a write rule", bin)}
		}
		// Any ReadOnlyMatcher hit = ALLOW.
		if matcherListMatches(rest, bp.ReadOnlyMatchers) {
			return Decision{Allow: true}
		}
		// Mixed default = REJECT (safer: ambiguous argv shouldn't run).
		return Decision{Allow: false, Reason: fmt.Sprintf("binary %q: argv does not match a known read-only form", bin)}
	case ClassNetwork:
		// Network class: WriteMatcher first (rare here — most network
		// binaries have no write semantics), then ReadOnlyMatcher
		// (preferred mode), then class default = ALLOW (host allowlist
		// is checked separately by Sandbox.Decide).
		if matcherListMatches(rest, bp.WriteMatchers) {
			return Decision{Allow: false, Reason: fmt.Sprintf("binary %q: argv matches a write rule", bin)}
		}
		// ReadOnlyMatchers are advisory for network — if present and
		// hit they document intent, but absence is OK.
		_ = bp.ReadOnlyMatchers
		return Decision{Allow: true}
	}
	return Decision{Allow: false, Reason: fmt.Sprintf("binary %q has unknown class %q", bin, bp.Class)}
}

// =====================================================================
// matcher helpers
// =====================================================================

// matcherListMatches is true when ANY matcher in the list hits argv.
func matcherListMatches(argv []string, matchers []ArgMatcher) bool {
	for _, m := range matchers {
		if matcherHits(argv, m) {
			return true
		}
	}
	return false
}

// matcherHits is true when the matcher's first non-empty rule matches.
// All-empty matchers are catch-alls (used in WriteMatchers fallbacks /
// mount's bare-form ReadOnly fallback).
func matcherHits(argv []string, m ArgMatcher) bool {
	allEmpty := len(m.AnyFlag) == 0 && m.Subcmd == "" && len(m.SubcmdPath) == 0
	if allEmpty {
		return true
	}
	if len(m.AnyFlag) > 0 && hasAnyFlag(argv, m.AnyFlag) {
		return true
	}
	if m.Subcmd != "" && len(argv) >= 1 && argv[0] == m.Subcmd {
		return true
	}
	if len(m.SubcmdPath) > 0 && hasSubcmdPath(argv, m.SubcmdPath) {
		return true
	}
	return false
}

func hasAnyFlag(argv []string, flags []string) bool {
	for _, a := range argv {
		for _, f := range flags {
			if a == f {
				return true
			}
		}
	}
	return false
}

func hasSubcmdPath(argv []string, want []string) bool {
	if len(argv) < len(want) {
		return false
	}
	for i, w := range want {
		if argv[i] != w {
			return false
		}
	}
	return true
}

// countAbsolutePaths counts argv tokens that look like absolute paths
// (i.e. start with "/"). Used by the mount heuristic.
func countAbsolutePaths(argv []string) int {
	n := 0
	for _, a := range argv {
		if strings.HasPrefix(a, "/") {
			n++
		}
	}
	return n
}
