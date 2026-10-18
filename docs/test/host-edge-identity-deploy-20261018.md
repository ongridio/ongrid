# Host / Edge 身份改造环境验收（2026-10-18）

## 部署范围

- 用户指定测试环境：`https://101.34.63.91/`。
- 来源：`fix/host-identity-tools`，基于 `3aee29ba` 的当前工作区；未推送、未合并。
- Manager、Web：`v0.18.2-host-identity-test.20261018.1`。
- 本机真实 Edge：`v0.18.2-host.20261018.1`。另一台 Edge 保持 `v0.15.0`，验证新旧客户端共存。
- 构建走根 Makefile；远端部署目录 `/opt/ongrid/test-deploy-20261018-host-identity-1`。
- 部署前已备份数据库、Compose 和原镜像引用；升级 Edge 前已备份原二进制。
- 最终两台 Host、两台 Edge 全部在线，Host 与当前 Edge 关联保持原值；首页、healthz、readyz 均 HTTP 200。

## 验证结果

| 范围 | 结果与证据 |
|---|---|
| 实际 API + 认证隧道 | 20 项通过，见 [acceptance.json](artifacts/host-identity-20261018/acceptance.json)。使用不同数字的 Host / Edge ID；探针模拟设备处理器，不执行 shell。 |
| 重装 / 旧实例 | 相同指纹保留 Host、新 Edge 接管；旧注册不能抢占；旧心跳不能恢复离线 Host；旧断开不影响当前在线状态；旧 Edge 执行请求被拒绝。 |
| 生命周期 | 删除所有安装仍保留 Host；Manager 重启仍保留；未注册 Edge 不再被补成同号 Host。测试 Host、Edge、工作流均清理成功。 |
| 工具与工作流 | get_host_summary、rank_hosts、find_outlier_hosts 及历史工具名兼容；离线摘要、技能按 Host 分派、拓扑分别统计 Host / Edge。 |
| 真实模型 | v2 / v3 分别实际调用 get_host_summary、rank_hosts，4 项通过；临时会话删除，默认运行时设置恢复。远端 runtime-acceptance.json。 |
| 真实设备 | 两台设备摘要和指标 API 返回数据；当前指标按 device_id 标记且新鲜度小于 120 秒；新版 Edge 通过 Host ID 执行真实 localhost ping。 |
| 告警 / 日志 / Trace | 两条实际告警关联到正确 Host；日志返回 5 个结果，device_id 均为 1；日志与 Trace 冲突 Host 条件均被拒绝。详见 [read-paths.json](artifacts/host-identity-20261018/read-paths.json)。 |
| Web | Chrome headless 完成登录，设备页、技能页 light / dark 截图实看，无 pageerror；Host 选择控件可用。截图见同目录 dark-devices、light-devices、dark-skill、light-skill.png。截图拍摄于真实 Edge 升级前，版本栏仍显示旧版。技能执行由 API/隧道验收覆盖，未声称浏览器点击执行已覆盖。 |
| 最终健康 | [final-health.json](artifacts/host-identity-20261018/final-health.json)。 |

## 测试中发现的问题与边界

- 本次长测试版本号超过既有 `agent_version varchar(32)` 限制，真实 Edge 注册写入失败。升级脚本自动恢复旧二进制，随后以短版本号重建相同源码并升级成功。未扩大生产 schema；任意长版本号支持仍是独立待办，正式发布前应限制构建版本长度或单独扩容字段。
- 初次指标验收脚本误传 Unix 时间，而接口要求 RFC3339；已修正脚本。指标工具返回 range 的 values，验收脚本已按实际响应读取。
- 新版 Edge 启动后插件按既有周期拉取配置；等待插件启动与采集恢复后才通过新鲜度检查。测试中 Manager 重启导致隧道短暂重连，最终两台设备恢复在线。
- 当前环境没有数据库指标源：仅验证 list_database_sources 返回 empty，以及 analyze_database_status 的缺少数据说明，不能算数据库有数据场景通过。
- Host 1 的 Trace 搜索无样本：只验证查询链路和冲突范围拒绝，未覆盖真实 Trace 内容归属。
- 两台现存设备的 Host / Edge ID 数值恰好相同。不同 ID、重装与竞态由真实隧道探针覆盖；没有向共享 Prometheus 写入不能清理的合成指标。重装后遥测历史连续性仍需有隔离遥测存储的完整采集环境验证。
- 未对第二台真实设备做卸载重装，未执行真实设备的破坏性命令，也未验证真实 WebSSH 交互。

## 重跑

先通过 `make build-host-identity-probe` 构建与部署机架构相同的探针，将 `tests/acceptance/hostidentity/run.py` 和探针传到测试机。脚本从现有 Manager 容器读取登录配置，不输出或保存凭据。

```bash
python3 /opt/ongrid/test-deploy-20261018-host-identity-1/acceptance.py \
  --url https://127.0.0.1 \
  --probe /opt/ongrid/test-deploy-20261018-host-identity-1/host-identity-probe \
  --restart-manager \
  --report /opt/ongrid/test-deploy-20261018-host-identity-1/acceptance-final.json
```

`--restart-manager` 会重启测试 Manager；普通复测可省略，避免打断会话。脚本只用于此类自签证书测试环境。

## 回滚

应用回滚使用已保存的原 Compose 与镜像：

```bash
python3 /opt/ongrid/test-deploy-20261018-host-identity-1/deploy.py rollback
```

这不会覆盖数据库。旧 Manager 包含原有身份补全逻辑，不应把回滚当作新旧身份模型可以随意混跑的保证；应先停止测试写入并评估数据状态。

Edge 回滚：将部署目录下 `ongrid-edge.before` 复制到 `/usr/local/bin/ongrid-edge.rollback`，设为 0755，再原子替换 `/usr/local/bin/ongrid-edge`，重启 `ongrid-edge.service`，核对 Host / Edge 身份和在线状态。不可直接覆盖运行中的可执行文件。

数据库备份 `database.before.sql.gz` 保留在权限受限的部署目录，仅在明确需要数据恢复时使用；常规应用回滚不恢复数据库。
