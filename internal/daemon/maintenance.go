package daemon

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/GerhardOfRivia/onderzeeer/internal/config"
	"github.com/GerhardOfRivia/onderzeeer/internal/queue"
)

// Maintenance runs on startup and hourly. It shares the queue connection with
// workers, and pruning releases it after each batch. Failures are logged and
// retried next hour without taking down discovery or execution.
func maintainQueue(ctx context.Context, store *queue.Store, cfg config.DatabaseConfig, logger *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	var previousWarnings string
	for {
		maintenancePass(ctx, store, cfg, logger, &previousWarnings)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func maintenancePass(ctx context.Context, store *queue.Store, cfg config.DatabaseConfig, logger *slog.Logger, previousWarnings *string) {
	if age := cfg.RetentionAge(); age > 0 {
		result, err := store.PruneOutput(ctx, queue.PruneOptions{Before: time.Now().UTC().Add(-age), IncludeFailed: cfg.RetentionIncludeFailed})
		if result.Commands > 0 {
			logger.Info("pruned expired captured output", "commands", result.Commands, "output_bytes", result.OutputBytes)
		}
		if err != nil && ctx.Err() == nil {
			logger.Warn("output retention failed; will retry", "error", err)
		}
	}
	if ctx.Err() != nil {
		return
	}
	info, err := store.Storage(ctx)
	if err != nil {
		logger.Warn("cannot inspect queue storage", "error", err)
		return
	}
	info.SetWarnings(cfg.WarningThresholds())
	// Log only changed warnings, rather than repeating an unchanged condition.
	warnings := strings.Join(info.Warnings, "\n")
	if warnings != *previousWarnings {
		for _, warning := range info.Warnings {
			logger.Warn("queue storage warning", "detail", warning, "database", cfg.Path)
		}
		*previousWarnings = warnings
	}
}
