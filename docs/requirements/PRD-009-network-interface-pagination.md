# PRD-009 大型交换机接口分页查询

关联 #374 的接口数量限制需求；手动添加 SNMP 设备不在本次范围。

## 用户需求

核心交换机可能有数百至数千个接口。助手和工作流应能查询完整的已保存 SNMP 接口快照，不再被前 100 条截断，同时避免单次工具结果过大。

## 行为与验收

- `query_network_interfaces` 新增非负 `offset` 参数，默认 0；`limit` 默认仍为 50，上限提高到 500，超限按 500 处理。
- 按名称、运行状态、异常接口筛选后，再按匹配结果分页；保留快照中的顺序。
- 保留已有 `interfaces`、`count`、设备信息及观察时间，新增 `total`、`offset`、`limit`、`has_more`、`next_offset`。
- `count` 表示本页数量，`total` 表示筛选后的总数。末页的 `has_more` 为 false，`next_offset` 为 null；超出末尾返回空数组，负 offset 返回错误。
- 1203 个接口可按 500、500、203 三页遍历，不重复、不遗漏；有筛选条件时总数与分页同样正确。
- 设备列表和邻居查询的上限保持原值；工具保持只读，不增加实时 SNMP 探测或凭据输出。

## 使用示例

```json
{"network_device_id":10,"limit":500,"offset":0,"only_attention":true}
```

若响应 `has_more` 为 true，保留同一设备、筛选条件和 limit，将 `offset` 设为返回的 `next_offset` 继续查询。快照可能在两次调用间被轮询刷新；需要一致遍历时比较 `last_observed_at`，发现变化应从第一页重查。本功能不锁定历史快照。

无 schema 或部署配置变更，可直接 revert 回滚；回滚后新增分页参数不再生效。
