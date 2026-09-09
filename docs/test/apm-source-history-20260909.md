# APM 源码完整同步与会话版本固定

## 修改范围

- 普通仓库同步获取所有远程分支、全部 Tag 和可达历史；已有单分支浅克隆通过 `--unshallow` 补齐，修正 fetch refspec。后续仍为增量 fetch。
- 文档索引继续使用配置的分支或 Tag，不把不同版本的文档混入同一知识索引。源码工具直接读取 Git 对象，不 checkout，也不逐版本复制目录。
- APM 创建会话时通过结构化 `apm_source` 请求传入 Trace ID 和所选服务范围；后端重新读取 Trace 与已保存的仓库绑定，解析实际失败 server span 的版本。客户端不能直接指定仓库或提交。
- 优先使用完整构建 SHA，缺少 SHA 时使用版本 Tag；Tag 与构建 SHA 冲突、缺少版本、多个匹配提交或源码不可用时，将失败原因保存到会话并阻止源码读取。遥测分析仍可继续。
- 将解析结果存入 `chat_sessions.apm_source`，后续轮次及子任务沿用固定 Commit。源码工具统一校验仓库、版本和源目录；通用非 APM 浏览可以显式指定 HEAD。
- 分析上下文不携带无关的 `synced_ref`（文档索引 Tag），并以服务端固定范围为准；已固定构建 SHA 时不要求额外存在 Tag。
- 源码工具输出真实行号，并修正末尾换行造成的 EOF 行数偏差。提示词要求用相同 operation、错误类型和输入条件判断历史缺陷，不能使用混合接口的 500 总量推断。

## 自动化验证

- `make proto`：通过，生成目录不提交。
- Go `-race`：APM 版本解析、知识库真实 Git 同步/读取、会话数据库持久化通过。
- Git 回归覆盖：已有浅克隆升级、默认索引 Tag 保持不变、新轻量/附注 Tag、其他分支、未打 Tag 的历史提交、首次完整克隆。
- Linux 专属包：本机 Zig/Go 交叉编译 `-race` 测试，在一次性容器中执行源码工具定向测试，以及 chatruntime、会话 service、HTTP 全包测试；通过。测试样例仅只读挂载，无业务数据连接。
- 前端：APM、绑定、错误 Trace 共 22 项测试通过；`make build-web` 通过。
- 定向 lint：`web/src/api/chat.ts` 原有 `while (true)` 触发 `no-constant-condition`，已核对 HEAD 中存在；其余本次检查文件通过。

## 边界与回滚

- 完整同步覆盖远程可获取分支和 Tag 的可达提交，不能恢复未推送或远程已清理的对象；不递归同步子模块源码或下载 Git LFS 实体。
- 会话范围是创建时的不可变快照。因未同步而受阻的会话，需要同步后重新发起分析；既有旧会话不自动改写。
- APM 会话内只读固定版本；历史对比应使用另一版本的独立分析会话，避免将其他版本源码当作当前实例证据。
- 新增 `apm_source` 为可空 TEXT 列，旧版本兼容；回滚管理端/前端镜像时保留列即可，无需删除历史证据。配套 up/down SQL 位于 `db/migrations/20260909170000_add_chat_apm_source.*.sql`。
- 全量历史增加首次同步时间和本地 Git 对象占用；尚未进行大型仓库容量验收。

## 本地运行验收

- 已部署本地镜像 `ongrid:dev-source-history` 和 `ongrid-web:dev-source-history`，运行标签为 `dev`；管理端镜像 SHA 为 `74d2094ad956f1a65d9976f175681c6f105cc66372e023bae83b8cda22d0a4ba`，healthz/readyz 通过。
- 先移除本地缓存中此前手工导入的两个演示 Tag，再通过页面点击同步。2026-09-09 16:29:19（Asia/Shanghai）同步成功，`is-shallow-repository=false`，`origin/main` 可见，完整历史 3 个提交。
- 文档索引 HEAD 保持 `9cf93884fa5d2f851dec73526bf5774c0e7d3731`（`1.1.0-demo`），索引文件数仍为 4。
- 自动恢复 `2.0.0-demo → d47b4ae49bdcef9ce58c39c6b924ded249467c90` 和 `2.1.0-demo → 989dbeb349e4d13fb56218bd675baba04d4349f0`；证据在 `output/apm-acceptance/source-history/sync.json`。仓库页面截图已实看。
- 本地 Compose 项目名为 `ongrid-native-deps`，部署必须显式传 `VERSION=dev` 和 `-p ongrid-native-deps`。最初遗漏参数的命令未替换运行实例，产生的空 `deploy_ongrid_repos` 卷已清理。

本地回滚（保留数据库新增可空列）：

```sh
docker tag ongrid:dev-before-source-history-20260909 ongrid:dev
docker tag ongrid-web:dev-before-source-history-20260909 ongrid-web:dev
make compose-up VERSION=dev \
  COMPOSE_ARGS='-p ongrid-native-deps -f deploy/docker-compose.yml -f output/native/deps.override.yml' \
  COMPOSE_SERVICES='--no-build --no-deps --pull never --force-recreate ongrid nginx'
```

### 双版本真实分析

- v1 Trace `1b43abef543e9430f450a4c097ef2204`，会话 `a39836b0-30e5-4a82-bf6c-e16a4504a37b`：后端固定 `d47b4ae4...`，实际源码工具定位 `pricing.go:19` 的折扣倍率错误，关联到相同 Trace 的 ERROR 日志。
- v2 Trace `921f12d6fac9eee48e9698b445b848e5`，首轮会话 `65bd22bb-9022-4e7b-85a5-88d2c8f2ad87`：后端固定 `989dbeb3...`，实际源码工具定位 `shipping.go:18` 大小写未归一化。模型对版本新增性明确表示无法判断，但误把 `synced_ref=1.1.0-demo` 当作缺少新版本源码的证据，因此又修正了前端分析上下文并补测试。
- 首轮原始分析记录保存在 `output/apm-acceptance/source-history/analysis-initial-p{1,2}.txt`。v2 首轮未完成 CPU/RSS/Go 运行时指标查询，模型明确列为数据缺口；这不等于全套遥测关联验收全部通过。

- 最终前端再次通过构建及错误 Trace 定向测试，已更新本地 nginx（ARM64 镜像）。新建 v2 会话 `fc2e82c1-0f3b-4988-b752-ff0e48f4f459` 已实测确认：`synced_ref` 不在分析上下文中，`commit_sha` 仍为 `989dbeb349e4d13fb56218bd675baba04d4349f0`，提示词明确已固定 Commit 不需要额外 Tag。

- 最终 v2 复测已完成：正确定位 `shipping.go:17–24` 中大小写敏感的费率表查询（具体查表语句位于第 18 行），结论不再声称文档索引 Tag 导致新版本源码不可用，明确单版本证据不足以判断新增性。第一次未按绑定数字 ID 读取，被范围校验拒绝（实际工具结果 `APM source: repo must be 1`，见 `source-guard-live.txt`），修正为绑定的 `repo_id=1` 后读取成功。
- 最终可查看会话：[v1](https://localhost:8443/chat/a39836b0-30e5-4a82-bf6c-e16a4504a37b)、[v2](https://localhost:8443/chat/fc2e82c1-0f3b-4988-b752-ff0e48f4f459)。最终记录及截图位于 `output/apm-acceptance/source-history/analysis-final-p{1,2}.{txt,png}`。
- 本次验证保证的是已测试路径上的仓库、提交与源码行号约束。模型对接口契约、运行时指标缺失、HTTP/RPC 覆盖等解释仍需核对实际工具证据，不能把源码定位通过等同于所有遥测推断正确。
- 修改仍在本地工作区，未提交或推送。
