package daemon

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GerhardOfRivia/onderzeeer/internal/config"
	"github.com/GerhardOfRivia/onderzeeer/internal/queue"
)

func TestMaintenanceRetentionIsOptInAndWarningsAreDeduplicated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Now().Add(-48 * time.Hour)
	path := filepath.Join(t.TempDir(), "queue.db")
	store, err := queue.Open(path, queue.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.Enqueue(ctx, queue.EnqueueParams{WatchName: "input", Path: "/input"}); err != nil {
		t.Fatal(err)
	}
	job, err := store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	command, err := store.StartCommand(ctx, queue.CommandStart{RunID: job.RunID, Program: "unused"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteCommand(ctx, command, queue.CommandResult{Status: queue.CommandSucceeded, Stdout: "saved"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Succeed(ctx, job.ID, job.RunID); err != nil {
		t.Fatal(err)
	}
	now = time.Now()
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	size, free := int64(1), float64(0)
	cfg := config.DatabaseConfig{Path: path, WarnSizeBytes: &size, WarnFreePercent: &free}
	var warnings string
	maintenancePass(ctx, store, cfg, logger, &warnings)
	commands, err := store.ListCommands(ctx, job.RunID)
	if err != nil || commands[0].Stdout != "saved" {
		t.Fatalf("default retention removed output: %+v %v", commands, err)
	}
	if !strings.Contains(output.String(), "queue storage warning") {
		t.Fatal("missing storage warning")
	}
	output.Reset()
	maintenancePass(ctx, store, cfg, logger, &warnings)
	if output.Len() != 0 {
		t.Fatalf("repeated warning: %s", output.String())
	}
	cfg.OutputRetention = &config.Duration{Duration: 24 * time.Hour}
	maintenancePass(ctx, store, cfg, logger, &warnings)
	commands, err = store.ListCommands(ctx, job.RunID)
	if err != nil || commands[0].Stdout != queue.PrunedOutputMarker {
		t.Fatalf("retention did not run: %+v %v", commands, err)
	}
	if !strings.Contains(output.String(), "pruned expired captured output") {
		t.Fatal("missing retention log")
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	done := make(chan struct{})
	go func() { maintainQueue(ctx, store, cfg, logger); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("maintenance did not stop on cancellation")
	}
}
