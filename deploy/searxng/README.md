# 搜索引擎运维

先从 Manager 所在环境执行实际 `/search?format=json&q=Kubernetes` 查询，确认至少一个带标题的 HTTP(S) 结果。HTTP 200 或首页可访问不足以证明搜索可用。同时检查 `unresponsive_engines`；timeout、CAPTCHA 和空结果是不同问题。

默认 settings.yml 保留通用部署行为。中国网络环境可参考 settings.cn.yml，将其 `use_default_settings` 和 `engines` 两段合并到部署配置，保留原 server/security 配置。不要用该片段直接覆盖整个配置。它显式限定 360search 和 sogou，不再继承其它默认引擎。抓取源可能随时触发验证码；2026-10-17 测试环境 360search 返回官方文档，搜狗部分请求 CAPTCHA，并非持续可用性保证。

修改前备份实际挂载文件；只重启 SearXNG 容器。修改后分别验证直接查询、设置页实际搜索测试、临时 Agent 会话及其返回链接。失败则恢复备份并重启。不要直接改用户会话或修改历史工具结果。

管理员在「设置 → 集成 → 联网搜索」选择主备来源和 SearXNG 引擎。备用默认关闭；确认允许发送查询内容并配置必要凭证后启用。正式 API 来源必须在该部署环境实际验证，不能仅依赖供应商名称或 API key 存在。

单来源预算和总预算不同于 SearXNG 内部引擎预算；工具层延长等待不会解除上游 CAPTCHA、封禁或网络不可达。连续失败后的冷却为单 Manager 进程内状态，下次查询触发恢复探测，不进行后台自动重放。设置页测试直接探测来源，不以缓存健康状态代替真实查询。
