// Package daemon wires filesystem discovery to the persistent queue and worker
// pool. The individual components remain independently testable and replaceable.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/GerhardOfRivia/onderzeeer/internal/config"
	"github.com/GerhardOfRivia/onderzeeer/internal/executor"
	"github.com/GerhardOfRivia/onderzeeer/internal/queue"
	"github.com/GerhardOfRivia/onderzeeer/internal/resource"
	"github.com/GerhardOfRivia/onderzeeer/internal/watcher"
	"github.com/GerhardOfRivia/onderzeeer/internal/worker"
)

const singleVersionFingerprint = "onderzeeer:path"

// Run starts discovery and execution and blocks until ctx is canceled or a
// component returns an error. Interrupted child processes are persisted as a
// failed attempt before workers stop whenever SQLite remains available.
func Run(ctx context.Context, cfg *config.Config, logger *slog.Logger) error {
	return RunWithResources(ctx, cfg, logger, &resource.Coordinator{})
}

// RunWithResources uses the caller's coordinator to share reservations across
// otherwise independent queues. The control daemon owns one for its lifetime.
func RunWithResources(ctx context.Context, cfg *config.Config, logger *slog.Logger, resources *resource.Coordinator) error {
	return run(ctx, cfg, logger, resources, false)
}

// RunTest runs standalone discovery and execution until canceled or a worker
// fails. The failed attempt is persisted before the error is returned.
func RunTest(ctx context.Context, cfg *config.Config, logger *slog.Logger) error {
	return run(ctx, cfg, logger, &resource.Coordinator{}, true)
}

func run(ctx context.Context, cfg *config.Config, logger *slog.Logger, resources *resource.Coordinator, stopOnError bool) error {
	if ctx == nil {
		return errors.New("daemon: context is required")
	}
	if cfg == nil {
		return errors.New("daemon: config is required")
	}
	if err := cfg.ValidateResources(); err != nil {
		return fmt.Errorf("daemon: validate config: %w", err)
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	for _, watch := range cfg.Watches {
		for index, command := range watch.Pipeline {
			for _, warning := range command.Warnings() {
				logger.Warn(warning, "watch", watch.Name, "step", index+1, "command", command.Name)
			}
		}
	}

	store, err := queue.Open(cfg.Database.Path)
	if err != nil {
		return err
	}
	defer store.Close()

	recovered, err := store.RecoverRunning(ctx)
	if err != nil {
		return err
	}
	if recovered > 0 {
		logger.Warn("recovered interrupted jobs", "jobs", recovered)
	}

	handleFile := func(ctx context.Context, watch config.WatchConfig, file watcher.File) error {
		fingerprint := file.Fingerprint
		if !watch.ReprocessOnChange {
			fingerprint = singleVersionFingerprint
		}
		job, created, err := store.Enqueue(ctx, queue.EnqueueParams{
			WatchName:   watch.Name,
			Path:        file.Path,
			Fingerprint: fingerprint,
			MaxRetries:  cfg.Queue.MaxRetries,
		})
		if err != nil {
			return err
		}
		if created {
			logger.Info("enqueued file", "job_id", job.ID, "watch", watch.Name, "file", file.Path)
		} else {
			logger.Debug("file already queued", "job_id", job.ID, "watch", watch.Name, "file", file.Path)
		}
		return nil
	}

	fileWatcher, err := watcher.New(cfg.Watches, handleFile, logger)
	if err != nil {
		return fmt.Errorf("daemon: create watcher: %w", err)
	}
	defer fileWatcher.Close()

	pool, err := worker.New(
		store,
		worker.NewConfigResolver(cfg.Watches),
		executor.NewLocal(logger),
		worker.Options{
			StopOnError: stopOnError,
			Workers:     cfg.Queue.Workers,
			Resources:   resources,
			RetryDelay:  cfg.Queue.RetryDelay.Duration,
			Logger:      logger,
		},
	)
	if err != nil {
		return fmt.Errorf("daemon: create workers: %w", err)
	}

	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		component string
		err       error
	}
	results := make(chan result, 2)
	go func() { results <- result{component: "watcher", err: fileWatcher.Run(runContext)} }()
	go func() { results <- result{component: "workers", err: pool.Run(runContext)} }()

	first := <-results
	cancel()
	second := <-results
	for _, outcome := range []result{first, second} {
		if outcome.err != nil && !isCancellationFrom(outcome.err, runContext.Err()) {
			return fmt.Errorf("daemon: %s: %w", outcome.component, outcome.err)
		}
	}
	return nil
}
