# ADR-033：LLM Wiki 架构

- 状态：已接受
- 日期：2026-09-11
- 关联 RFC：[RFC-004：LLM Wiki 编译、存储与检索实现](../rfc/RFC-004-llm-wiki.md)
- 关联 HLD：[HLD-002：LLM Wiki 可审计知识编译层](../design/HLD-002-llm-wiki.md)

## 背景

LLM Wiki 同时包含四条需要长期稳定的架构边界：

1. 编译管线如何把 Source 转换为页面。
2. Raw、版本、页面 artifact 与 catalog 分别由什么存储负责。
3. MySQL/SQLite 双方言如何共享 schema 和词法检索语义。
4. 页面向量放在哪里，以及与词法检索如何配合。

## 决策

### 1. 使用类型化、线性编译管线

`buildCompiler.Compile` 固定按以下顺序执行：

1. 创建 `staging` Build。
2. `loadCorpus` 读取 Source 和 Version。
3. `prepareCorpusChunks` 读取快照并切块。
4. `summarizer.Summarize` 生成 digest。
5. `planner.Plan` 生成页面计划。
6. `evidenceResolver.Resolve` 解析 section 来源。
7. `pageWriter.WritePage` 和 `artifactStore.writePageBody` 写页面。
8. `validateBuild` 校验 artifact。
9. `indexBuildPages` 更新词法和向量索引。
10. `Publish` 在事务中激活 build。

包内类型包括 `corpus`、`digestItem`、`plan`、`resolvedPage`、`WikiBuildPage` 和 `IndexDocument`。当前不存在 `sourceIR/parseIR/semanticIR/knowledgeIR`。

### 2. FileStore 与应用数据库分离

FileStore 保存：

- `raw/` 当前 Raw 文件。
- `.llm-wiki/versions/<source-hash>/<sha256>.md` 不可变版本快照。
- `wiki/builds/<build-id>/pages/<page-id>.md` 生成页面 artifact。

应用数据库保存：

- Source、Version 和 CompileJob。
- Build 和 BuildPage catalog。
- `wiki_lexical`。

应用数据库是 job、build 和来源关系的权威 catalog；FileStore 是 Raw、版本和页面 artifact 的权威存储；Qdrant 是派生向量索引。

已删除的表（2026-09-20，从 `db/migrations/20260918100000_add_llm_wiki_tables` 与 `store.Migrate` 移除）：

- `wiki_source_chunks`：从未有写入路径，chunk 只存在于编译期内存和 build 页面里。
- `wiki_index_meta`：只写不读，索引重建实际以 Qdrant 是否已有向量为准。

### 3. 跟随 `ONGRID_DB_DIALECT`

- `managerllmwikidata.Open` 接收共享 `*gorm.DB`。
- Wiki 不创建独立 `.llm-wiki/wiki.db`，不拥有连接池。
- `store.Migrate` 只接受 `mysql` 和 `sqlite`。
- MySQL 当前 schema 位于 `db/migrations/20260918100000_add_llm_wiki_tables`。
- SQLite 设置 `foreign_keys=ON`、`busy_timeout=5000`、`journal_mode=WAL`。
- 词法索引使用普通表 `wiki_lexical` 和 `LIKE ... ESCAPE '!'`，不使用 SQLite FTS5 或 MySQL 专有全文索引。

### 4. 页面向量使用 Qdrant

- collection：`ongrid_llm_wiki`
- point ID：`sha256("wiki:<tenant_id>:<page_id>")` 前 8 字节
- payload：tenant 字符串、page_id、page_type、title、body_hash
- 查询强制 tenant filter
- `body_hash` 未变化时跳过 embedding
- 配置 embedder 时 Qdrant 是硬依赖
- 向量失败只影响派生索引，不被词法结果掩盖
- 词法与向量结果使用 RRF 常数 60 融合

## 一致性边界

- active build 切换在应用数据库事务中完成。
- 页面 artifact 通过 SHA-256 校验。
- 新 build 激活后删除旧 build artifact；旧 Build/BuildPage 数据库行保留为 `superseded`。
- 文件系统和应用数据库之间不存在跨存储事务。
- 当前 `Reconcile` 只从 SourceVersion 快照恢复缺失 Raw 文件，不重放编译任务。
- Qdrant 和 `wiki_lexical` 都可重建。

## 备选方案

### 单个无类型编译函数

降低文件数量，但错误边界、清理逻辑和增量继承无法由类型表达。

### 五层公共 IR

隔离更强，但当前代码没有对应类型，也没有跨包复用需求。

### 独立 SQLite 文件

有利于单目录携带，但与共享 MySQL 的部署、迁移和备份边界冲突。

### 数据库内 BLOB 向量和进程内余弦

少一个外部依赖，但需要线性扫描，无法利用 Qdrant HNSW。

### 复用旧 RAG collection

会混合页面级与 chunk 级 point，payload 和重建边界不同。

## 后果

- 编译流程固定且可审计，但当前不支持动态 DAG。
- Wiki catalog 跟随应用数据库，FileStore 仍需独立持久化和备份。
- 配置 embedder 后，Qdrant 成为 Wiki 的运行依赖。
- 词法检索是全表 `LIKE`，规模上限需要和向量检索一起评估。
- Source 的 `succeeded` 状态没有生产赋值路径。
- `force_compile` 字段存在，但当前编译代码没有读取。
- usage session 会把每个成功 compiler LLM 调用的 usage 写入现有 AIOps chat transcript；记录失败只告警，不阻断 Wiki 编译。
- `manager_metrics.go` 中的 LLM Wiki collectors 当前没有被编译代码更新。
- OpenAI-compatible embedding 用量和 Wiki 编译调用的每日 token budget 不在当前范围内。
