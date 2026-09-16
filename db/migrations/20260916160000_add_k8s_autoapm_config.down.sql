-- Export cluster capture settings before rollback; dropping the column loses them.
ALTER TABLE k8s_clusters DROP COLUMN auto_apm_config_json;
