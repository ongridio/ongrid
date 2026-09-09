# 仓库保存前验证与索引分支编辑

## 行为

- 新增及编辑均由后端在落库前运行 Git `ls-remote`，使用与同步相同的 SSH 凭证和 known_hosts，验证可访问以及指定分支/Tag 确实存在。验证约 20 秒超时，Git 子进程管道关闭最多另等待 10 秒。
- 只接受 HTTP(S)、SSH（含 SCP 风格）URL；拒绝本地路径、file/ext 协议、内嵌 HTTP 凭证或 SSH 密码、URL 查询参数和控制字符。索引引用使用不带 `refs/` 的分支或 Tag 名；同名分支和 Tag 产生歧义时拒绝保存。
- 新增验证失败不创建记录；编辑验证失败不改原配置或索引。验证通过不等于完整克隆或 embedding 成功，后续结果由仓库卡片展示。
- 新增 PATCH `/v1/knowledge/repos/{id}`，沿用仓库写权限和审计。只编辑索引分支/Tag 及说明；保留 URL、仓库 ID、APM 绑定和全部源码历史。拒绝请求修改 URL。
- 编辑成功后清空同步尝试时间并唤醒已有后台工作器，自动重建文档索引。同步/删除/编辑共用同仓库并发保护。
- 复用同一弹窗，新旧配置预填；“验证并保存”过程中禁用输入和重复提交。失败显示提示及原始原因并保留输入，成功关闭。旧“保存后再点同步”文案已移除。

## 测试

- `go test -race ./internal/manager/biz/knowledge ./internal/manager/server/knowledge` 通过。
- 实际临时 HTTP Git 仓库 + SQLite 验证：新建访问拒绝、缺失分支、危险输入不落库；编辑失败保留配置/索引；编辑成功保持 ID/URL/源码统计并自动切换索引提交；并发编辑受阻。
- HTTP 测试覆盖 PATCH 路由及传参、URL 不可变校验、创建接口非法 URL 拦截。
- 前端 5 项测试通过，涵盖新增失败保留输入、编辑预填和 URL 禁用、失败后改正并成功保存。
- 定向 ESLint、`make proto`、`git diff --check` 通过。

## 回滚

本次无需新增数据库字段。已备份本地镜像，回滚保留原有数据卷：

```sh
docker tag ongrid:dev-before-repo-config-20260909 ongrid:dev
docker tag ongrid-web:dev-before-repo-config-20260909 ongrid-web:dev
make compose-up VERSION=dev \
  COMPOSE_ARGS='-p ongrid-native-deps -f deploy/docker-compose.yml -f output/native/deps.override.yml' \
  COMPOSE_SERVICES='--no-build --no-deps --pull never --force-recreate ongrid nginx'
```

## 本地部署与验收

- 管理端构建通过，已部署 `ongrid:dev-repo-config`（镜像 `d932ff9bc507b91e32e8af2d128e72850536ed1c65f4cefa06ee478a084581f8`）。
- 切换管理端期间短暂出现 502，启动完成后 healthz/readyz 恢复 `ok`/`ready`；原仓库和自动同步仍在。

- 前端镜像构建通过并部署，SHA `f2472370777005432140266a5353ffa31721f5010bb2f51b230af6453edd9eb5`。
- 真实私有 SSH 仓库从 `1.1.0-demo` 编辑至 `2.1.0-demo` 后自动同步成功，原仓库 ID 1 的 Git HEAD 为 `989dbeb349e4d13fb56218bd675baba04d4349f0`；仍是 3 Commit、4 Tag、1 分支、4 个索引文件。随后已通过同一编辑流程恢复 `1.1.0-demo`。
- 无效编辑保持已保存配置不变、URL 禁用、输入保留。实测发现 SSH 的普通 stderr 提示会掩盖“分支不存在”，已改用 `ls-remote --exit-code` 的退出码 2 识别缺失引用，并加入回归测试；相关包重新 `-race` 通过。

- 最终管理端镜像为 `048aaa9c814e3020bb1730f04e70aec91e01b206cf359c5b33a3b81a20344308`，包含缺失引用退出码识别修复。
- 修复后真实新增验证返回 `branch or tag "missing-acceptance-20260909" not found`，表单保留输入，页面仓库数仍为 1（`create-rejected.json`）。改为 `1.1.0-demo` 后新增成功，并自动索引 4 个文件，显示 3 Commit / 4 Tag / 1 分支（`create-synced.json`）。
- 临时登记（URL 为不带 `.git` 的同一演示仓库）已通过页面移除。最终页面仅原仓库，缓存仅 `/var/lib/ongrid/repos/1`，HEAD 恢复 `9cf93884fa5d2f851dec73526bf5774c0e7d3731`；healthz 通过。
- 截图及记录在 `output/apm-acceptance/repo-config/`。新建失败提示与成功卡片截图已实看，表单输入和操作按钮均可见。
- 本次改动未提交或推送。

- 编辑弹窗深色截图已实看，完成后恢复“跟随系统”主题，保留最终仓库页面。
