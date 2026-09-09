# Go 双版本错误 Trace 与源码定位验证

日期：2026-09-09。范围：本机私有 apm-demo Git、Ubuntu 的两个 Go 演示实例、本地 Ongrid/Tempo/Prometheus/ES；不代表生产验收。

## 真实版本与构建

| 实例 | Tag / service.version | Commit |
| --- | --- | --- |
| ubuntu-go-1 | 2.0.0-demo | d47b4ae49bdcef9ce58c39c6b924ded249467c90 |
| ubuntu-go-2 | 2.1.0-demo | 989dbeb349e4d13fb56218bd675baba04d4349f0 |

两个 Tag 已发布到本机私有 `apm-demo` 仓库，没有移动原有 `1.0.0` / `1.1.0-demo` Tag。两个 Linux ARM64 二进制从各自确切提交交叉编译，版本和 Commit 经链接参数写入；`/build`、Trace Resource、JSON 响应和日志携带构建标识。Trace 与指标使用同一个 OTel Resource，优先使用编译版本覆盖环境声明。

当前源码是第二版，含用于演示的地区匹配缺陷；不要用于真实支付或生产订单。所有目录、价格和授权都是内存演示数据，没有真实扣款。

## 相同请求，不同错误

请求：`GET /checkout/42?coupon=SAVE20&region=eu&quantity=2`。

- 2.0.0-demo：`pricing.go:19` 把 20% 折扣写成减去 20 倍。5000 分减去 100000 分得到 -95000 分，`payment.authorize` 拒绝非正金额。5 个 Span，未进入配送报价。
- 2.1.0-demo：`pricing.go:19` 已修复为 `subtotal * 20 / 100`，支付金额 4000 分；`shipping.go:18` 直接用小写 `eu` 查仅含 `EU`/`US` 的表，匹配失败。6 个 Span，支付授权成功、配送报价失败。
- 成功对照：两个版本的 `?region=US` 都返回 200、总价 5500 分；新版 `?coupon=SAVE20&region=EU&quantity=2` 返回 200、4900 分（单元测试）。输入数量与地区保留边界验证。

## 首次错误样本

| 版本 | Trace ID | Ongrid 分析会话 |
| --- | --- | --- |
| 2.0.0-demo | 90744b7c0fa24c0215c3cbbef0bc8737 | [旧版分析](https://localhost:8443/chat/d22ad429-3ba8-40cc-8426-74cbd762a892) |
| 2.1.0-demo | ee687470ea85174e726026e005c1c678 | [新版分析](https://localhost:8443/chat/8a831d43-6333-4efe-9b47-657c93bf7081) |

发生时间：07:46:45 UTC / 15:46:45 上海时间。两条 Trace 均已从 Tempo 实际读取。Prometheus 按 `service_instance_id` / `service_version` 查得两条实例分组，与 Trace 和 `/build` 一致。Ongrid 已配置的日志工具各命中一条同 Trace 的 ERROR 日志。

Go 服务现绑定 repo_id=1、目录 `examples/apm-go`、Tag 规则 `{version}`。首次 AI 分析从 Span 的完整构建 SHA 读取正确版本代码，分别命中折扣公式和地区匹配问题。首次回答存在引用行号偏差，且新版回答曾从聚合 500 比例相似错误推断缺陷在发布前已存在；这些不构成版本回归证据，需以两个 Tag 源码和具体操作对比为准。

补充复核通过：两个分析会话分别实际使用 `refs/tags/2.0.0-demo` / `refs/tags/2.1.0-demo` 读取源码，工具响应 SHA 与上述构建 SHA 完全一致。旧版折扣引用纠正为 `pricing.go:19`，新版匹配引用为 `shipping.go:18`；两份回答均撤回无源码对比支持的缺陷引入时间断言。保存的工具结果见 `tag-tool-p1.txt` / `tag-tool-p2.txt`。

## 部署与复现

现场复用既有 Go 镜像的运行环境，两个不同二进制只读挂载到 `/app/app`：

- `/opt/ongrid-apm-demo/releases/2.0.0-demo/apm-go`
- `/opt/ongrid-apm-demo/releases/2.1.0-demo/apm-go`

覆盖文件为 `/opt/ongrid-apm-demo/compose.versions.yaml`；流量文件为 `/opt/ongrid-apm-demo/traffic.versions.py`。流量生成器持续为两个 Go 版本生成上述结算成功/失败请求，沿用低频约 5% / 20% 的失败请求配置；其他语言保持原有请求路径。失败比例由请求组合决定，不证明缺陷历史或版本间总体质量。

从根 Makefile 构建任一版本，`APM_GO_SOURCE` 必须是该 Tag 的干净检出；例：

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 GOMAXPROCS=2 GOFLAGS=-p=2 \
make build-apm-go APM_GO_SOURCE=/path/to/clean/tag-checkout \
  BIN_DIR=/tmp/ongrid-go-versions/2.0.0-demo \
  LDFLAGS='-s -w -X main.version=2.0.0-demo -X main.commit=d47b4ae49bdcef9ce58c39c6b924ded249467c90'

make compose-up COMPOSE='orb -m ubuntu sudo docker compose' \
  COMPOSE_ARGS='-f /opt/ongrid-apm-demo/compose.yaml -f /opt/ongrid-apm-demo/compose.versions.yaml' \
  COMPOSE_SERVICES='--no-build --no-deps go-v1 go-v2 traffic'
```

回滚：使用同一根目标，只保留基础 `/opt/ongrid-apm-demo/compose.yaml`，重建 `go-v1 go-v2 traffic`，即可恢复原镜像二进制与原流量；无数据库或其他语言变更。

## 验证边界

- 两个版本的 Go 单元测试均使用 `-race` 通过，覆盖实际错误子 Span、成功路径和非法输入。
- 现有代码仓库“同步”只拉配置的默认分支/Tag，不会自动同步这些新 Tag。本次先正常同步，再通过仅含已发布代码的 Git bundle 导入两个指定 Tag，保留原 HEAD；不能将此报告描述为新 Tag 自动发现验收。
- 源码工具显式传入精确 Tag 时不可用就失败，不回退；通用工具省略 revision 仍可读 HEAD。选择正确 revision 和固定后续 SHA 仍由 Agent 提示约束，本次成功不等于所有模型永远不会选错。
- 原始 Trace JSON、版本指标结果及测试输出位于 `output/apm-acceptance/go-versions/`。
