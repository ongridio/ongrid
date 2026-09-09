# APM 设备筛选与统一集群验收

## 实现范围

- APM 列表及详情增加设备、集群筛选。筛选作用于聚合和分页之前，覆盖原生 HTTP/RPC 指标、Tempo 样本指标、运行时、实例、诊断及告警模板。
- `device_id` 是设备主键；`cluster_node_id` 是统一拓扑集群主键。两者同时选择时取交集。
- 设备集群通过 `member_of` 和设备表 `devices.node_id` 解析当前成员；不依赖设备名称或拓扑节点的 `device_id` 属性。删除的设备和节点不参与解析，空集群不会退化为全量查询。
- Kubernetes 集群通过拓扑节点的 `k8s_cluster_id` 转换为遥测 `cluster_id`，不把两个 ID 空间混用。
- 后端返回解析后的范围，Trace 跳转使用同一范围；范围尚未加载时不提供无范围的 Trace 查询。服务依赖图仍为服务级数据，有资源筛选时提示清除筛选后查看。
- Tempo spanmetrics 保留 `device_id`、`cluster_id`。该配置影响之后生成的指标，不回填历史序列。
- 设备集群按当前成员关系查询历史时间窗；现有模型没有成员归属历史。

## 统一集群入口

- 集群列表同时展示设备和 Kubernetes 集群，以“接入方式”区分。
- 新建入口复用设备接入、Kubernetes 接入表单。
- `/clusters/:clusterId` 使用统一拓扑 ID，根据接入方式复用已有详情。
- Kubernetes 详情保留升级、Token 轮换、卸载命令和删除确认；设备集群继续使用原有成员、批次及升级管理。
- 侧栏统一为“集群”。旧 `/kubernetes` 列表地址转至 `/clusters`，旧 Kubernetes 详情地址继续兼容。
- 集群列表不提供 APM 跳转；Kubernetes 行右侧保留管理、升级命令、卸载命令和删除快捷入口，操作列固定在右侧。

## 自动验证

- Go `-race`：APM、拓扑、设备存储、APM HTTP 四个包通过。
- 前端：APM/API、集群列表/模型、Kubernetes 共 76 项测试通过。后续快捷命令调整后，集群页 13 项测试、TypeScript、ESLint 和前端构建通过；升级及卸载弹窗已实际打开核验并关闭，未执行命令。
- 设备存储测试覆盖空拓扑属性、同名节点、设备主键与节点主键不同、删除设备、删除节点、空范围。
- 统一集群测试覆盖设备成员范围、Kubernetes ID 转换、空集群、未知集群、组合条件、告警范围和依赖图限制。
- TypeScript、ESLint、proto 生成、`git diff --check` 通过。

## 本地环境与数据

- 入口：`https://localhost:8443`，Compose 项目 `ongrid-native-deps`。
- 设备 `650`（物理机）对应拓扑节点 `100`，属于设备集群 `101`（测试集群）。该设备节点属性为空，已用真实数据发现并修复兼容问题。
- Kubernetes 集群拓扑节点 `95`（vm-kubeadm-fresh-pull）对应遥测集群 ID `48`。
- 示例服务：apm-demo-go、apm-demo-java、apm-demo-python，原生指标和新生成的 Trace 指标均带设备 `650`。
- 两种接入弹窗已实际打开并取消；没有改动现有成员关系或新建验收数据。

## 页面验收与部署

- 设备集群 101、设备 650、两者组合：均实际返回三个示例服务；清除集群保留设备，清除两者恢复全部四个服务。
- 服务详情 HTTP/RPC 指标及实例资源正常；返回列表保留组合条件，Trace 链接包含设备 650 和解析后的集群成员条件。
- Kubernetes 集群 95 返回零服务，符合当前数据；该集群现处于离线状态。正向 K8s APM 数据场景尚未实测，ID 转换及查询条件由自动测试覆盖。
- 统一列表同时显示两种接入方式；Kubernetes 管理详情、升级及轮换入口、卸载/删除菜单可用。未执行这些管理动作。旧 `/kubernetes` 入口实际跳转至 `/clusters`。
- 浅色、深色截图已检查。390px 窄屏下折叠侧栏后筛选完整可用，表格横向滚动；展开侧栏仍会挤压内容，这是当前全局布局限制。验收后恢复跟随系统主题和展开侧栏。
- 本地 `/readyz` 返回 `ready`；前端通过根 Makefile 原生构建，再通过 Makefile 打包和更新本地 nginx。
- 后端镜像：`sha256:af7f3d2c606c9c4657fc5f86240dd8ad24fe639bc24ba1a0f8604b5fa27a1c82`。
- 前端镜像：`sha256:b25fd19b4016ed0a7ca7dc721df136bafe9668a677fbf38e3bc4df36bb25c806`。

证据目录：`output/apm-acceptance/resource-scope/`。

## 回滚

保留 `ongrid:dev-before-apm-resource-20260909` 和 `ongrid-web:dev-before-apm-resource-20260909`。
回滚时将它们重新标记为 `:dev`，用当前 Compose 文件和 `output/native/deps.override.yml` 重建 `ongrid nginx`，保留现有卷。
Tempo 新增两个维度可独立撤回；无需数据库迁移或数据回填。
