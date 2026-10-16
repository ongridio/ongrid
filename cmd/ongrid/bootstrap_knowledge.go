package main

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"

	managerbizknowledge "github.com/ongridio/ongrid/internal/manager/biz/knowledge"
	managerknowledgedata "github.com/ongridio/ongrid/internal/manager/data/knowledge/store"
	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/embedding"
	"github.com/ongridio/ongrid/internal/pkg/qdrantx"
)

type knowledgeModule struct {
	embErr      error
	qdrantURL   string
	knowledgeUC *managerbizknowledge.Usecase
}

func bootstrapKnowledge(rootCtx context.Context, cfg *config.Config, log *slog.Logger, storage *storageModule, settings *settingsModule, telemetry *telemetryModule, aiops *aiopsModule) *knowledgeModule {
	// Knowledge base + git-repo integration (RAG Phase-1). Wire BEFORE
	// buildAIOpsRuntime so the BaseTool bag picks up query_knowledge —
	// SetKnowledgeSearcher only affects subsequent BuildBaseTools calls.
	if err := managerknowledgedata.Migrate(storage.db); err != nil {
		log.Error("knowledge: migrate failed", slog.Any("err", err))
	}
	knowledgeRepo := managerknowledgedata.New(storage.db)
	telemetry.apmService.WithRepositoryBindings(settings.settingSvc, knowledgeRepo)
	// Embedding provider — defaults to OpenAI-compatible API
	// (works for OpenAI, GLM, Qwen, DeepSeek). Falls back to the
	// existing OPENAI_API_KEY when ONGRID_EMBEDDING_API_KEY is empty
	// (most operators just have one provider configured).
	embAPIKey := os.Getenv("ONGRID_EMBEDDING_API_KEY")
	if embAPIKey == "" {
		embAPIKey = cfg.OpenAI.APIKey
	}
	embBaseURL := os.Getenv("ONGRID_EMBEDDING_BASE_URL")
	if embBaseURL == "" {
		embBaseURL = cfg.OpenAI.BaseURL
	}
	embDim := 1536
	if v := os.Getenv("ONGRID_EMBEDDING_DIM"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			embDim = n
		}
	}
	embedder, embErr := embedding.New(embedding.Config{
		Provider: os.Getenv("ONGRID_EMBEDDING_PROVIDER"),
		Model:    os.Getenv("ONGRID_EMBEDDING_MODEL"),
		BaseURL:  embBaseURL,
		APIKey:   embAPIKey,
		Dim:      embDim,
		Log:      log.With(slog.String("comp", "embedding")),
	})
	qdrantURL := os.Getenv("ONGRID_QDRANT_URL")
	if qdrantURL == "" {
		qdrantURL = "http://qdrant:6333"
	}
	var knowledgeUC *managerbizknowledge.Usecase
	{
		qdrantClient := qdrantx.New(qdrantURL, log.With(slog.String("comp", "qdrant")))
		// Build with a nil embedder when one isn't configured — the
		// usecase exposes read paths (ListDocs/Repos/GetDoc/ListPaths)
		// and gates write paths (CreateManualDoc/Sync/Search) on
		// embed != nil so the SPA's 知识库 / 代码仓库 pages render on
		// fresh install instead of 404'ing. Operator configures
		// ONGRID_EMBEDDING_API_KEY later → writes unblock without
		// restart-of-stack (only the manager needs the key on boot).
		var maybeEmbedder embedding.Embedder
		if embErr != nil {
			log.Warn("knowledge: embedder unavailable — reads enabled, writes disabled",
				slog.Any("err", embErr))
		} else {
			maybeEmbedder = embedder
		}
		uc, kErr := managerbizknowledge.New(rootCtx, knowledgeRepo, qdrantClient, maybeEmbedder,
			os.Getenv("ONGRID_KNOWLEDGE_REPO_DIR"),
			log.With(slog.String("comp", "knowledge")))
		if kErr != nil {
			log.Warn("knowledge: usecase build failed", slog.Any("err", kErr))
		} else {
			knowledgeUC = uc
			go knowledgeUC.RunAutoSync(rootCtx)
			aiops.toolsReg.SetKnowledgeSearcher(knowledgeUC)
			telemetry.apmService.WithSourceRevisions(knowledgeUC)
			// GitHub-PAT-via-GIT_ASKPASS resolver wiring
			// removed. SSH-style repos use ssh_identities; HTTPS auth
			// returns in P3 via credential.helper.
			// Built-in vault seed (ADR-029) — default-on, source fixed to
			// the public github.com/ongridio/vault with the embedded
			// snapshot as the offline fallback. The source is NOT operator-
			// configurable: the old ONGRID_BUILTIN_VAULT_URL "point it at a
			// git mirror" path was removed because it registered the vault as
			// a knowledge_repos row and leaked it into the 代码仓库 / Repos
			// list — Repos is for user code the Agent analyzes, never platform
			// content. Set ONGRID_BUILTIN_VAULT_SEED=off to skip seeding (tests).
			//
			// Why default-on: empty knowledge bases at first boot were
			// repeatedly mistaken for "RAG broke" — the operator expects at
			// least the platform playbooks to be there. The background sync
			// (cloud clone, embedded fallback) must not stall the HTTP
			// listener, so it runs in a goroutine and only when the vault
			// isn't already indexed. The Knowledge page "云端同步" button
			// re-runs the same SyncBuiltinVault path on demand.
			if seed := strings.TrimSpace(os.Getenv("ONGRID_BUILTIN_VAULT_SEED")); seed == "-" || strings.EqualFold(seed, "off") {
				log.Info("knowledge: built-in vault seed disabled via env")
			} else {
				// Migrate away any legacy vault repo row (pre-ADR-029 installs
				// seeded the vault AS a repo) so it stops lingering in Repos.
				if purged, pErr := knowledgeUC.PurgeBuiltinVaultRepo(rootCtx); pErr != nil {
					log.Warn("knowledge: purge legacy vault repo", slog.Any("err", pErr))
				} else if purged {
					log.Info("knowledge: migrated built-in vault off the repos table")
				}
				go knowledgeUC.SeedBuiltinVaultAtBoot(log)
			}
		}
	}

	return &knowledgeModule{
		embErr:      embErr,
		qdrantURL:   qdrantURL,
		knowledgeUC: knowledgeUC,
	}
}
