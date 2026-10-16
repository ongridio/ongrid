// host_netns_inspect.go is the structured network-namespace inspector.
//
// Why this exists. The bash sandbox accepts `ip netns list` (read) and
// `ip netns identify` but cannot express the grammar of `ip -n <ns>
// <subcmd>` — the global option `-n <ns>` shifts argv past the
// SubcmdPath/Subcmd matchers (see cmdpolicy/policy.go's `ip` block).
// As a result `host_bash cmd="ip -n foo addr show"` lands in the
// "argv does not match a known read-only form" reject path. This skill
// fills that gap: it builds the right argv internally, runs read-only
// `ip -j -n <ns> ...` per namespace, and returns structured JSON the
// LLM can consume directly.
//
// Threat model. Skill code runs as root on the edge. `namespace` param
// is filtered through validateNetnsName before reaching argv so an LLM
// passing `; rm -rf /` style strings can't escape into a shell — the
// param sits in os/exec's argv slice (no shell), and is rejected up
// front anyway by the regex.
//
// Result shape (JSON):
//
//	{
//	  "namespaces": [
//	    {
//	      "name": "foo",
//	      "addrs":  [{iface, family, addr, prefix}, ...],
//	      "routes": [{dst, gateway?, iface}, ...],
//	      "links":  [{iface, state, mac}, ...]   // only when include_links=true
//	      "error":  "..."  // per-ns error; other ns still populate
//	    }
//	  ]
//	}
package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/skill"
)

func init() { skill.Register(&HostNetnsInspect{}) }

// HostNetnsInspect lists network namespaces and reports per-ns network
// state (addresses + routes + optional link info).
type HostNetnsInspect struct{}

// Metadata returns the framework-visible spec.
func (HostNetnsInspect) Metadata() skill.Metadata {
	return skill.Metadata{
		Key:         "host_netns_inspect",
		Name:        "网络命名空间探查",
		Description: "列出 /var/run/netns 下的所有 network namespace 并对每个 namespace 报告 IP 地址 / 路由 / 接口状态。填补 host_bash 不支持 `ip -n <ns>` 的盲区。仅 read-only。",
		Class:       skill.ClassSafe,
		Scope:       skill.ScopeHost,
		Category:    "network",
		Params: skill.ParamSchema{
			{Name: "namespace", Param: skill.Param{
				Type: "string",
				Desc: "可选：只查这一个 namespace 名（精确匹配）。留空 = 列全部。仅允许 [a-zA-Z0-9_.-]，最长 64 字符。",
			}},
			{Name: "include_routes", Param: skill.Param{
				Type:    "bool",
				Default: true,
				Desc:    "是否带回每个 ns 的路由表。默认 true。",
			}},
			{Name: "include_links", Param: skill.Param{
				Type:    "bool",
				Default: false,
				Desc:    "是否带回每个 ns 的 link 列表（含 MAC / state）。默认 false（多 ns 时数据量大）。",
			}},
		},
		ResultPreview: "{namespaces:[{name, addrs:[...], routes:[...], links?:[...], error?}]}",
	}
}

type netnsParams struct {
	Namespace     string `json:"namespace"`
	IncludeRoutes *bool  `json:"include_routes"` // pointer to distinguish unset from false
	IncludeLinks  bool   `json:"include_links"`
}

type netnsAddr struct {
	Iface  string `json:"iface"`
	Family string `json:"family"`
	Addr   string `json:"addr"`
	Prefix int    `json:"prefix"`
}

type netnsRoute struct {
	Dst     string `json:"dst"`
	Gateway string `json:"gateway,omitempty"`
	Iface   string `json:"iface,omitempty"`
}

type netnsLink struct {
	Iface string `json:"iface"`
	State string `json:"state"`
	MAC   string `json:"mac,omitempty"`
}

type netnsRecord struct {
	Name   string       `json:"name"`
	Addrs  []netnsAddr  `json:"addrs,omitempty"`
	Routes []netnsRoute `json:"routes,omitempty"`
	Links  []netnsLink  `json:"links,omitempty"`
	Error  string       `json:"error,omitempty"`
}

type netnsResult struct {
	Namespaces []netnsRecord `json:"namespaces"`
	Error      string        `json:"error,omitempty"`
}

// nameRE is the namespace-name allowlist (iproute2's own naming rules
// are looser, but locking ourselves to alphanumeric + _ - . prevents
// any shell-metacharacter sneaking into argv even though we use os/exec
// (no shell). Length cap at 64 matches Linux NAME_MAX-ish for kernel
// objects.
var nameRE = regexp.MustCompile(`^[a-zA-Z0-9_.\-]{1,64}$`)

func validateNetnsName(s string) error {
	if !nameRE.MatchString(s) {
		return fmt.Errorf("namespace name %q invalid (allowed: [a-zA-Z0-9_.-], 1..64 chars)", s)
	}
	return nil
}

// Execute lists namespaces (or filters to one) and inspects each.
func (HostNetnsInspect) Execute(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	var p netnsParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("host_netns_inspect: decode params: %w", err)
		}
	}
	// Validate name FIRST (security boundary — must run regardless of
	// host OS so tests on darwin can exercise the rejection path).
	if p.Namespace != "" {
		if err := validateNetnsName(p.Namespace); err != nil {
			return nil, fmt.Errorf("host_netns_inspect: %w", err)
		}
	}
	if runtime.GOOS != "linux" {
		return json.Marshal(netnsResult{Error: "host_netns_inspect: only linux supported"})
	}

	includeRoutes := true
	if p.IncludeRoutes != nil {
		includeRoutes = *p.IncludeRoutes
	}

	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	var names []string
	if p.Namespace != "" {
		names = []string{p.Namespace}
	} else {
		listed, err := listNetns(cctx)
		if err != nil {
			// `ip netns` not installed or no /var/run/netns — return
			// empty namespaces with the error string so the LLM sees
			// "no ns found" rather than tool-call failure.
			return json.Marshal(netnsResult{Error: err.Error()})
		}
		names = listed
	}

	result := netnsResult{Namespaces: make([]netnsRecord, 0, len(names))}
	for _, name := range names {
		rec := netnsRecord{Name: name}
		addrs, err := readAddrs(cctx, name)
		if err != nil {
			rec.Error = err.Error()
			result.Namespaces = append(result.Namespaces, rec)
			continue
		}
		rec.Addrs = addrs
		if includeRoutes {
			routes, rerr := readRoutes(cctx, name)
			if rerr != nil {
				rec.Error = rerr.Error()
			} else {
				rec.Routes = routes
			}
		}
		if p.IncludeLinks {
			links, lerr := readLinks(cctx, name)
			if lerr != nil && rec.Error == "" {
				rec.Error = lerr.Error()
			} else {
				rec.Links = links
			}
		}
		result.Namespaces = append(result.Namespaces, rec)
	}
	return json.Marshal(result)
}

// listNetns parses `ip netns list` text output. Each line is
// "<name>" optionally followed by " (id: N)". Empty stdout → no ns.
func listNetns(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, "ip", "netns", "list").Output()
	if err != nil {
		// ExitError with empty stdout is common when no ns exists; be
		// tolerant. Real "command not found" or permission errors return
		// here.
		if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
			// Run still produced output — pass through.
			if len(out) > 0 {
				goto parse
			}
		}
		return nil, fmt.Errorf("ip netns list: %w", err)
	}
parse:
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	out2 := make([]string, 0, len(lines))
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		// "<name> (id: 1)" → take field[0]
		fields := strings.Fields(ln)
		if len(fields) > 0 {
			if err := validateNetnsName(fields[0]); err == nil {
				out2 = append(out2, fields[0])
			}
		}
	}
	return out2, nil
}

// readAddrs runs `ip -j -n <ns> addr show` and parses the JSON output.
// iproute2's JSON shape: array of interface objects with addr_info[].
func readAddrs(ctx context.Context, ns string) ([]netnsAddr, error) {
	out, err := exec.CommandContext(ctx, "ip", "-j", "-n", ns, "addr", "show").Output()
	if err != nil {
		return nil, fmt.Errorf("ip -j -n %s addr show: %w", ns, err)
	}
	var raw []struct {
		IfName   string `json:"ifname"`
		AddrInfo []struct {
			Family    string `json:"family"`
			Local     string `json:"local"`
			PrefixLen int    `json:"prefixlen"`
		} `json:"addr_info"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("decode addr json: %w", err)
	}
	var addrs []netnsAddr
	for _, iface := range raw {
		for _, a := range iface.AddrInfo {
			addrs = append(addrs, netnsAddr{
				Iface:  iface.IfName,
				Family: a.Family,
				Addr:   a.Local,
				Prefix: a.PrefixLen,
			})
		}
	}
	return addrs, nil
}

// readRoutes runs `ip -j -n <ns> route show` and parses the JSON output.
func readRoutes(ctx context.Context, ns string) ([]netnsRoute, error) {
	out, err := exec.CommandContext(ctx, "ip", "-j", "-n", ns, "route", "show").Output()
	if err != nil {
		return nil, fmt.Errorf("ip -j -n %s route show: %w", ns, err)
	}
	var raw []struct {
		Dst     string `json:"dst"`
		Gateway string `json:"gateway"`
		Dev     string `json:"dev"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("decode route json: %w", err)
	}
	routes := make([]netnsRoute, 0, len(raw))
	for _, r := range raw {
		dst := r.Dst
		if dst == "" {
			// iproute2 omits "dst" for default route.
			dst = "default"
		}
		routes = append(routes, netnsRoute{
			Dst:     dst,
			Gateway: r.Gateway,
			Iface:   r.Dev,
		})
	}
	return routes, nil
}

// readLinks runs `ip -j -n <ns> link show` and parses the JSON output.
func readLinks(ctx context.Context, ns string) ([]netnsLink, error) {
	out, err := exec.CommandContext(ctx, "ip", "-j", "-n", ns, "link", "show").Output()
	if err != nil {
		return nil, fmt.Errorf("ip -j -n %s link show: %w", ns, err)
	}
	var raw []struct {
		IfName   string `json:"ifname"`
		Operstate string `json:"operstate"`
		Address   string `json:"address"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("decode link json: %w", err)
	}
	links := make([]netnsLink, 0, len(raw))
	for _, l := range raw {
		links = append(links, netnsLink{
			Iface: l.IfName,
			State: l.Operstate,
			MAC:   l.Address,
		})
	}
	return links, nil
}
