// Package hostmetrics 是 edge 侧的 hostmetrics plugin。
//
// 通过 subprocess 采集主机指标并暴露 Prometheus 端点供 manager 抓取。
//
// 平台切换通过 build tag：
//   - Linux（plugin_linux.go）：subprocess node_exporter，监听 :9102
//   - Windows（plugin_windows.go）：subprocess windows_exporter，监听 127.0.0.1:9182
//
// 采集后端：Linux 用 node_exporter，Windows 用 windows_exporter（plugin_linux.go / plugin_windows.go）。
package hostmetrics

// Name 是 OTel 对齐的 plugin 名称；匹配 manager 侧的 PluginNameHostMetrics
// 和 <workDir>/plugins/ 下的目录 key。
const Name = "hostmetrics"
