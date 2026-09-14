package queue

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestInterruptedResourceWaitPreservesHistoryAndRetryBudget(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job, _, err := store.Enqueue(ctx, EnqueueParams{WatchName: "incoming", Path: "/input", MaxRetries: 1})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	commandID, err := store.StartCommand(ctx, CommandStart{RunID: first.RunID, Sequence: 1, Name: "prepare", Program: "unused"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteCommand(ctx, commandID, CommandResult{Status: CommandSucceeded}); err != nil {
		t.Fatal(err)
	}
	if err := store.InterruptResourceWait(ctx, job.ID, first.RunID, "waiting for gpu: context canceled"); err != nil {
		t.Fatal(err)
	}
	if err := store.InterruptResourceWait(ctx, job.ID, first.RunID, "again"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("duplicate interruption: %v", err)
	}
	commands, err := store.ListCommands(ctx, first.RunID)
	if err != nil || len(commands) != 1 || commands[0].Status != CommandSucceeded {
		t.Fatalf("history changed: %+v, %v", commands, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != job.ID || second.Attempt != 2 || second.MaxRetries != 1 {
		t.Fatalf("requeued job: %+v", second)
	}
	status, err := store.Fail(ctx, second.ID, second.RunID, "execution failed", 0)
	if err != nil || status != StatusQueued {
		t.Fatalf("canceled wait spent retry budget: %s, %v", status, err)
	}
	third, err := store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	status, err = store.Fail(ctx, third.ID, third.RunID, "execution failed", 0)
	if err != nil || status != StatusFailed {
		t.Fatalf("retry budget not enforced: %s, %v", status, err)
	}
	runs, err := store.ListRuns(ctx, job.ID)
	if err != nil || len(runs) != 3 {
		t.Fatalf("runs: %+v, %v", runs, err)
	}
}

func TestOpeningExistingQueueAddsResourceWaitBookkeeping(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE resource_wait_interruptions`); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.Enqueue(ctx, EnqueueParams{WatchName: "old", Path: "/existing", MaxRetries: 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err := store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	status, err := store.Fail(ctx, job.ID, job.RunID, "failure", 0)
	if err != nil || status != StatusFailed {
		t.Fatalf("legacy retry behavior changed: %s, %v", status, err)
	}
}
