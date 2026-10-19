-- MySQL：扩展 Host 长期配置表。应用在校验 current_edge_id 与双向关联后接纳旧配置。
CREATE TABLE IF NOT EXISTS host_plugin_configs (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 device_id BIGINT UNSIGNED NOT NULL,
 plugin_name VARCHAR(32) NOT NULL,
 removed BOOLEAN NOT NULL DEFAULT FALSE,
 enabled BOOLEAN NOT NULL,
 spec_json TEXT NOT NULL,
 created_at DATETIME(3) NULL,
 updated_at DATETIME(3) NULL,
 UNIQUE KEY uk_host_plugin (device_id, plugin_name)
);
