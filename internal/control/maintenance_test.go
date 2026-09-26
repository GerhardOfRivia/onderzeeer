package control

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GerhardOfRivia/onderzeeer/internal/config"
	"github.com/GerhardOfRivia/onderzeeer/internal/queue"
)

func TestRemoteQueueMaintenanceAndStorage(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path, database := filepath.Join(root, "worker.yaml"), filepath.Join(root, "queue.db")
	cfg := testConfig(database)
	size, free := int64(1), float64(0)
	cfg.Database.WarnSizeBytes, cfg.Database.WarnFreePercent = &size, &free
	_, client, socket := newUnixTransportHarness(t, Options{Loader: mappedLoader(t, map[string]*config.Config{path: cfg}), Runner: waitingRunner})
	ctx := context.Background()
	instances, err := client.Start(ctx, []string{path}, "worker")
	if err != nil {
		t.Fatal(err)
	}
	store, err := queue.Open(database, queue.WithClock(func() time.Time { return time.Now().Add(-48 * time.Hour) }))
	if err != nil {
		t.Fatal(err)
	}
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
	store.Close()
	reader := NewQueueReader(socket, instances[0].ID)
	defer reader.Close()
	info, err := reader.Storage(ctx)
	if err != nil || info.DatabaseBytes == 0 || len(info.Warnings) != 1 {
		t.Fatalf("storage: %+v, %v", info, err)
	}
	system, err := client.System(ctx)
	if err != nil || len(system.QueueStorage) != 1 || len(system.QueueStorage[0].Storage.Warnings) != 1 {
		t.Fatalf("system: %+v, %v", system, err)
	}
	options := queue.PruneOptions{Before: time.Now().Add(-time.Hour), DryRun: true}
	preview, err := reader.PruneOutput(ctx, options)
	if err != nil || preview.Commands != 1 || preview.OutputBytes != 5 {
		t.Fatalf("preview: %+v, %v", preview, err)
	}
	commands, err := reader.ListCommands(ctx, job.RunID)
	if err != nil || commands[0].Stdout != "saved" {
		t.Fatalf("dry run modified output: %+v, %v", commands, err)
	}
	options.DryRun = false
	result, err := reader.PruneOutput(ctx, options)
	if err != nil || result.Commands != 1 {
		t.Fatalf("prune: %+v, %v", result, err)
	}
	if err := reader.Compact(ctx); err == nil {
		t.Fatal("compacted running instance")
	}
	if _, err := client.Stop(ctx, "worker"); err != nil {
		t.Fatal(err)
	}
	if err := reader.Compact(ctx); err != nil {
		t.Fatal(err)
	}
	commands, err = reader.ListCommands(ctx, job.RunID)
	if err != nil || commands[0].Stdout != queue.PrunedOutputMarker {
		t.Fatalf("pruned output: %+v, %v", commands, err)
	}
	if _, err := reader.PruneOutput(ctx, queue.PruneOptions{}); err == nil {
		t.Fatal("accepted invalid cutoff")
	}
	if err := os.Remove(database); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.PruneOutput(ctx, options); err == nil {
		t.Fatal("pruned missing database")
	}
	if err := reader.Compact(ctx); err == nil {
		t.Fatal("compacted missing database")
	}
	if _, err := os.Stat(database); !os.IsNotExist(err) {
		t.Fatalf("maintenance recreated missing database: %v", err)
	}
}
