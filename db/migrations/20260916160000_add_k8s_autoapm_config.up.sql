-- MySQL 8.0.12+; apply before deploying Manager. Older versions ignore this column.
ALTER TABLE k8s_clusters
    ADD COLUMN auto_apm_config_json TEXT NULL,
    ALGORITHM=INSTANT;
