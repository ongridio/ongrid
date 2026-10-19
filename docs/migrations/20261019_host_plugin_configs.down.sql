-- 仅用于首次升级失败且尚未接受 Host 配置写入的回退。
-- 已写入时先停止 Manager 并导出配置；禁止直接删除后运行旧版本。
DROP TABLE IF EXISTS host_plugin_configs;
