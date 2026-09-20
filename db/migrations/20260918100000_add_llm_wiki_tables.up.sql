-- LLM Wiki catalog and derived search tables on the shared application
-- database. Mirrors the GORM models under
-- internal/manager/model/knowledge/llm_wiki; MySQL 8.0 InnoDB, utf8mb4.
CREATE TABLE IF NOT EXISTS wiki_sources (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    tenant_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
    source_key VARCHAR(512) NOT NULL DEFAULT '',
    source_type VARCHAR(32) NOT NULL DEFAULT '',
    raw_path VARCHAR(1024) NOT NULL DEFAULT '',
    current_version_id BIGINT UNSIGNED NULL,
    content_sha256 VARCHAR(64) NOT NULL DEFAULT '',
    status VARCHAR(24) NOT NULL DEFAULT 'pending',
    created_at DATETIME(3) NULL,
    updated_at DATETIME(3) NULL,
    deleted_at DATETIME(3) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_wiki_source (tenant_id, source_key),
    KEY idx_wiki_source_status (tenant_id, status),
    KEY idx_wiki_source_deleted (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS wiki_source_versions (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    tenant_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
    source_id BIGINT UNSIGNED NOT NULL,
    sha256 VARCHAR(64) NOT NULL DEFAULT '',
    size_bytes BIGINT UNSIGNED NOT NULL DEFAULT 0,
    snapshot_path VARCHAR(1024) NOT NULL DEFAULT '',
    schema_version VARCHAR(32) NOT NULL DEFAULT 'v1',
    created_at DATETIME(3) NULL,
    updated_at DATETIME(3) NULL,
    deleted_at DATETIME(3) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_wiki_source_version (tenant_id, source_id, sha256),
    KEY idx_wiki_version_source (tenant_id, source_id),
    KEY idx_wiki_version_deleted (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS wiki_compile_jobs (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    tenant_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
    active_key VARCHAR(64) NULL,
    status VARCHAR(24) NOT NULL DEFAULT 'pending',
    stage VARCHAR(32) NOT NULL DEFAULT 'queued',
    force_compile TINYINT(1) NOT NULL DEFAULT 0,
    source_ids VARCHAR(2048) NOT NULL DEFAULT '',
    lease_owner VARCHAR(128) NOT NULL DEFAULT '',
    lease_expires_at DATETIME(3) NULL,
    attempt INT UNSIGNED NOT NULL DEFAULT 0,
    cancel_requested TINYINT(1) NOT NULL DEFAULT 0,
    error_message VARCHAR(2048) NOT NULL DEFAULT '',
    created_at DATETIME(3) NULL,
    updated_at DATETIME(3) NULL,
    deleted_at DATETIME(3) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_wiki_job_active (active_key),
    KEY idx_wiki_job_claim (tenant_id, status, lease_expires_at),
    KEY idx_wiki_job_deleted (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS wiki_builds (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    tenant_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
    status VARCHAR(24) NOT NULL DEFAULT 'staging',
    page_count INT NOT NULL DEFAULT 0,
    error_msg TEXT NOT NULL,
    created_at DATETIME(3) NULL,
    activated_at DATETIME(3) NULL,
    PRIMARY KEY (id),
    KEY idx_wiki_build_tenant_status (tenant_id, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS wiki_build_pages (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    build_id BIGINT UNSIGNED NOT NULL,
    tenant_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
    page_id VARCHAR(64) NOT NULL DEFAULT '',
    page_type VARCHAR(24) NOT NULL DEFAULT 'generated',
    title VARCHAR(512) NOT NULL DEFAULT '',
    body_path VARCHAR(1024) NOT NULL DEFAULT '',
    body_sha256 VARCHAR(64) NOT NULL DEFAULT '',
    source_refs_json JSON NOT NULL,
    created_at DATETIME(3) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_wiki_build_page (build_id, page_id),
    KEY idx_wiki_build_page_tenant (tenant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Derived search table: rebuildable from build pages and artifacts.
CREATE TABLE IF NOT EXISTS wiki_lexical (
    page_key VARCHAR(96) NOT NULL,
    tenant_id BIGINT UNSIGNED NOT NULL,
    page_id VARCHAR(64) NOT NULL,
    page_type VARCHAR(24) NOT NULL DEFAULT '',
    title VARCHAR(512) NOT NULL DEFAULT '',
    aliases VARCHAR(512) NOT NULL DEFAULT '',
    content LONGTEXT NOT NULL,
    PRIMARY KEY (page_key),
    KEY idx_wiki_lexical_tenant (tenant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
