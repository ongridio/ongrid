// 此文件无 build tag — 在 Windows + Linux 上都编译。
// #18-3 catalog metadata 单一真相源
// 问题（重构前）：3 个 Windows skill 的 metadata 定义在两处：
//   - windows/<skill>.go（//go:build windows）— Windows 端真实 skill
//   - windows_catalog_other.go（//go:build !windows）— Linux 端 stub
// 新增/修改 skill 时必须手动同步两处，catalog metadata 偏差 = 静默生产 bug。
// 解决方案：metadata 是纯数据（skill.Metadata struct），不依赖任何 Windows API。
// 提取为无 build tag 的导出函数，两侧共用同一函数调用。
//   - Windows 端：<skill>.Metadata() 委托 XxxMetadata()
//   - Linux 端：windows_catalog_other.go 调用 windows.XxxMetadata()

package windows

import (
	"github.com/ongridio/ongrid/internal/skill"
)

// 钳制边界/默认值常量 — 与 event_log.go 共用（消除重复字面量）
const (
	defaultMaxEvents = 100
	maxMaxEvents     = 1000
	defaultSince     = "1h"
	defaultLevel     = "Error"
)

// EventLogMetadata 是 host_windows_event_log skill 的单一真相源。
// Windows 端 EventLog.Metadata() 和 Linux 端 catalog stub 都调用此函数。
func EventLogMetadata() skill.Metadata {
	return skill.Metadata{
		Key:         "host_windows_event_log",
		Name:        "Windows 事件日志查询",
		Description: "查 Windows EventLog（System/Application/Security/Setup/ForwardedEvents）。返回最近 N 条匹配级别的事件。",
		Class:       skill.ClassSafe,
		Scope:       skill.ScopeHost,
		Category:    "log",
		Params: skill.ParamSchema{
			{Name: "log_name", Param: skill.Param{
				Type: "enum", Required: true,
				Enum: []string{"System", "Application", "Security", "Setup", "ForwardedEvents"},
				Desc: "事件日志名称",
			}},
			{Name: "max_events", Param: skill.Param{
				Type: "int", Default: defaultMaxEvents,
				Desc: "返回最大条数，默认 100",
			}},
			{Name: "level", Param: skill.Param{
				Type: "enum", Default: defaultLevel,
				Enum: []string{"Critical", "Error", "Warning", "Information", "Verbose"},
				Desc: "事件级别过滤",
			}},
			{Name: "since", Param: skill.Param{
				Type: "duration", Default: defaultSince,
				Desc: "查询时间范围（Go duration），默认 1h",
			}},
			{Name: "include_message", Param: skill.Param{
				Type: "bool", Default: true, //  Layer 2：默认带 Message（culture 已强制 en-US）
				Desc: "是否返回 Message 字段（已强制 en-US 渲染）。批量摘要场景可设 false 省 token",
			}},
		},
		ResultPreview: "{events: [{id, provider, level, message, time_created}], total}",
	}
}

// ServicesMetadata 是 host_windows_services skill 的单一真相源。
func ServicesMetadata() skill.Metadata {
	return skill.Metadata{
		Key:         "host_windows_services",
		Name:        "Windows 系统服务查询",
		Description: "查 Windows 系统服务列表（Get-Service）。可按名称/状态/启动类型过滤，返回服务名/显示名/状态/启动类型/依赖链。只读操作。",
		Class:       skill.ClassSafe,
		Scope:       skill.ScopeHost,
		Category:    "service",
		Params: skill.ParamSchema{
			{Name: "name", Param: skill.Param{
				Type: "string", Required: false,
				Desc: "按服务名精确过滤（如 Spooler），留空返回全部",
			}},
			{Name: "status", Param: skill.Param{
				Type: "enum", Default: "all",
				Enum: servicesStatusEnumList,
				Desc: "按运行状态过滤，默认 all",
			}},
			{Name: "start_type", Param: skill.Param{
				Type: "enum", Default: "all",
				Enum: servicesStartTypeEnumList,
				Desc: "按启动类型过滤，默认 all",
			}},
		},
		ResultPreview: "[{name, display_name, status, start_type, dependencies}]",
	}
}

// ProcessesMetadata 是 host_windows_processes skill 的单一真相源。
func ProcessesMetadata() skill.Metadata {
	return skill.Metadata{
		Key:         "host_windows_processes",
		Name:        "Windows 进程查询",
		Description: "查 Windows 进程列表（Get-Process）。按 CPU 降序取前 N，可按名称/最小内存过滤，返回进程名/PID/CPU秒数/内存MB/路径。只读操作。",
		Class:       skill.ClassSafe,
		Scope:       skill.ScopeHost,
		Category:    "process",
		Params: skill.ParamSchema{
			{Name: "name", Param: skill.Param{
				Type: "string", Required: false,
				Desc: "按进程名精确过滤（如 chrome），留空返回全部",
			}},
			{Name: "top_n", Param: skill.Param{
				Type: "int", Default: 10,
				Desc: "按 CPU 排序取前 N，默认 10，范围 [1,100]",
			}},
			{Name: "min_memory_mb", Param: skill.Param{
				Type: "int", Default: 0,
				Desc: "只返回工作集大于 N MB 的进程，默认 0（不过滤）",
			}},
		},
		ResultPreview: "[{name, id, cpu_seconds, working_set_mb, path}]",
	}
}

// HotfixMetadata 是 host_windows_hotfix skill 的单一真相源。
func HotfixMetadata() skill.Metadata {
	return skill.Metadata{
		Key:         "host_windows_hotfix",
		Name:        "Windows 补丁查询",
		Description: "查 Windows 已安装补丁列表（Get-HotFix）。按安装时间降序取前 N，可过滤近 N 天内安装的，返回 KB 号/描述/安装时间/来源/安装者。只读操作。",
		Class:       skill.ClassSafe,
		Scope:       skill.ScopeHost,
		Category:    "patch",
		Params: skill.ParamSchema{
			{Name: "top_n", Param: skill.Param{
				Type: "int", Default: 20,
				Desc: "返回最大条数（按 InstalledOn 降序），默认 20，范围 [1,200]",
			}},
			{Name: "since_days", Param: skill.Param{
				Type: "int", Default: 0,
				Desc: "只返回近 N 天内安装的补丁，默认 0（不过滤），范围 [0,365]",
			}},
		},
		ResultPreview: "[{hotfix_id, description, installed_on, source, installed_by}]",
	}
}

// NetworkMetadata 是 host_windows_network skill 的单一真相源。
func NetworkMetadata() skill.Metadata {
	return skill.Metadata{
		Key:         "host_windows_network",
		Name:        "Windows TCP 连接查询",
		Description: "查 Windows TCP 连接列表（Get-NetTCPConnection）。可按状态/本地端口/远程地址/远程端口过滤，返回本地/远程地址+端口+状态+归属 PID。只读操作。",
		Class:       skill.ClassSafe,
		Scope:       skill.ScopeHost,
		Category:    "network",
		Params: skill.ParamSchema{
			{Name: "state", Param: skill.Param{
				Type: "enum", Default: "all",
				Enum: networkStateEnumList,
				Desc: "按 TCP 状态过滤，默认 all",
			}},
			{Name: "local_port", Param: skill.Param{
				Type: "int", Default: 0,
				Desc: "按本地端口过滤（精确匹配），默认 0（不过滤）",
			}},
			{Name: "remote_address", Param: skill.Param{
				Type: "string", Required: false,
				Desc: "按远程地址精确过滤（如 192.168.1.1），留空不过滤",
			}},
			{Name: "remote_port", Param: skill.Param{
				Type: "int", Default: 0,
				Desc: "按远程端口过滤（精确匹配），默认 0（不过滤）",
			}},
			{Name: "top_n", Param: skill.Param{
				Type: "int", Default: 50,
				Desc: "返回最大条数，默认 50，范围 [1,500]",
			}},
		},
		ResultPreview: "[{local_address, local_port, remote_address, remote_port, state, owning_process}]",
	}
}
