package knowledge

import (
	"context"
	"log/slog"
	"time"
)

// SeedBuiltinVaultAtBoot indexes the built-in vault only when it is not already present.
func (u *Usecase) SeedBuiltinVaultAtBoot(log *slog.Logger) {
	syncCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if u.HasVaultDocs(syncCtx) {
		log.Info("knowledge: built-in vault already indexed — skipping boot sync")
		return
	}
	if n, src, sErr := u.SyncBuiltinVault(syncCtx); sErr != nil {
		log.Warn("knowledge: initial vault sync failed — operator can retry from UI",
			slog.Any("err", sErr))
	} else {
		log.Info("knowledge: initial vault sync ok",
			slog.Int("file_count", n), slog.String("source", src))
	}
}
