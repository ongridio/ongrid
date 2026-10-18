# metrics last_error 恢复语义修复

## 问题

真实 Edge 重装验收发现：采集目标启动时暂未监听，metrics 插件写入 `health.LastError`；目标恢复后端点返回 HTTP 200，但成功抓取、上报路径没有清除错误。单个共享 LastError 也无法表达多个采集目标的独立恢复。

## 修改

- 按目标 URL 维护当前抓取错误和上报错误，LastError 汇总所有尚未恢复的目标，稳定排序。
- 成功抓取仅清除同一目标的抓取错误，成功上报仅清除同一目标的上报错误。
- 空响应或 Edge 尚未注册时没有上报尝试，因此不清除历史上报错误。
- 新配置移除目标时清除其错误，并忽略旧目标在途返回；保留仍配置目标的状态。
- 取消运行的 context 不记为采集失败。
- Start 在启动 goroutine 前设置运行状态，避免首轮失败被稍后的初始化清空。
- 不修改 Manager API、协议或数据库；需要更新 Edge 二进制。

## 单元与竞态测试

Linux builder 执行 `go test -race ./internal/edgeagent/plugins/metrics ./internal/edgeagent/plugins` 通过。

新增测试使用真实本地 HTTP 服务及可控上报器，覆盖双目标失败、部分恢复、全部恢复、恢复后再次失败、其他目标成功不掩盖上报失败、空响应/未注册不能清除上报失败、配置移除目标、取消及启动首轮失败。

## 部署

- 测试环境 `101.34.63.91`，仅更新本机 Edge 至 `v0.18.2-health.20261018.1`；Manager / Web 未变。
- 升级后 Host 1 → Edge 14 保持不变，旧二进制保留用于回滚。
- 二进制 SHA256：`a58b86265d364e1da1ad3a5673fc8ae7a0ab47b12ac2d42cfcb3b1e69121b3a7`，上传后校验一致。
- 现场目录 `/opt/ongrid/test-edge-health-20261018/`，包含 deploy.py、acceptance.py、原 metrics 配置、旧二进制与验收报告。
- 回滚方式：停止服务或采用临时路径原子替换，将该目录的 `ongrid-edge.before` 恢复为 `/usr/local/bin/ongrid-edge` 后重启 `ongrid-edge.service`；保持当前 Edge 14 凭据，不重建身份。
- 本次未修改首次插件配置约 60 秒轮询等待的启动行为。

## 现场故障注入结果

通过两个临时 localhost HTTP 目标依次制造 503 → 部分恢复 → 全部恢复 → 再次失败 → 移除失败目标，观察 Manager 插件健康 API 的心跳结果，六个检查全部通过：

1. 新版启动后的真实采集恢复，LastError 为空。
2. 两个目标均失败时同时显示两项错误。
3. 第一个目标恢复后仅保留第二个目标错误。
4. 全部恢复后 LastError 清空。
5. 恢复后再次失败能够重新显示。
6. 配置移除仍失败目标后不残留错误。

[完整现场证据](artifacts/metrics-health-20261018/acceptance.json)。原 metrics enabled/spec 已恢复并核对一致，临时 HTTP 服务已关闭。临时端点恢复时返回空的合法响应，未向共享 Prometheus 写入合成指标。上报失败及其恢复由单元测试覆盖，本轮没有人为中断共享 Manager 隧道。

未推送、未合并。
