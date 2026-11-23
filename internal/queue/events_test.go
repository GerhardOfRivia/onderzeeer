package queue

import (
	"context"
	"path/filepath"
	"testing"
)

func TestJobTransitionsAppendMonotonicEvents(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()

	job, created, err := store.Enqueue(ctx, EnqueueParams{
		WatchName: "incoming", Path: "/drop/a.csv", Fingerprint: "v1", ExternalID: "batch-7", MaxRetries: 1,
	})
	if err != nil || !created {
		t.Fatal(err)
	}
	if _, created, err := store.Enqueue(ctx, EnqueueParams{
		WatchName: "incoming", Path: "/drop/a.csv", Fingerprint: "v1", ExternalID: "other",
	}); err != nil || created {
		t.Fatalf("duplicate created=%v err=%v", created, err)
	}
	claimed, err := store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := store.Fail(ctx, claimed.ID, claimed.RunID, "transient", 0); err != nil || status != StatusQueued {
		t.Fatalf("retry status=%s err=%v", status, err)
	}
	claimed, err = store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Succeed(ctx, claimed.ID, claimed.RunID); err != nil {
		t.Fatal(err)
	}

	events, err := store.ListEvents(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{EventJobEnqueued, EventJobStarted, EventJobRetried, EventJobStarted, EventJobSucceeded}
	if len(events) != len(want) {
		t.Fatalf("events = %+v", events)
	}
	for i, event := range events {
		if event.Seq != int64(i+1) || event.Type != want[i] || event.JobID != job.ID || event.Path != "/drop/a.csv" || event.ExternalID != "batch-7" {
			t.Fatalf("event %d = %+v", i, event)
		}
		if event.CreatedAt.IsZero() || event.JobCreatedAt.IsZero() || event.UpdatedAt.IsZero() {
			t.Fatalf("missing timestamps: %+v", event)
		}
	}
	if events[0].Status != StatusQueued || events[0].Attempts != 0 || events[0].StartedAt != nil {
		t.Fatalf("enqueue event = %+v", events[0])
	}
	if events[1].Status != StatusRunning || events[1].Attempts != 1 || events[1].StartedAt == nil {
		t.Fatalf("start event = %+v", events[1])
	}
	if events[2].Status != StatusQueued || events[2].Type != EventJobRetried {
		t.Fatalf("retry event = %+v", events[2])
	}
	if events[4].Status != StatusSucceeded || events[4].FinishedAt == nil {
		t.Fatalf("success event = %+v", events[4])
	}
	page, err := store.ListEvents(ctx, 3, 1)
	if err != nil || len(page) != 1 || page[0].Seq != 4 {
		t.Fatalf("page = %+v err=%v", page, err)
	}
	if _, err := store.ListEvents(ctx, -1, 1); err == nil {
		t.Fatal("negative cursor accepted")
	}
}

func TestFailureAndInterruptionEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	failed, _, err := store.Enqueue(ctx, EnqueueParams{WatchName: "incoming", Path: "/drop/fail", MaxRetries: 0})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := store.Fail(ctx, claimed.ID, claimed.RunID, "nope", 0); err != nil || status != StatusFailed {
		t.Fatalf("fail status=%s err=%v", status, err)
	}

	waiting, _, err := store.Enqueue(ctx, EnqueueParams{WatchName: "incoming", Path: "/drop/wait"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err = store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPending(ctx, claimed.ID, claimed.RunID); err != nil {
		t.Fatal(err)
	}
	if err := store.InterruptResourceWait(ctx, claimed.ID, claimed.RunID, "canceled"); err != nil {
		t.Fatal(err)
	}

	recoveredStore := openTestStore(t)
	if _, _, err := recoveredStore.Enqueue(ctx, EnqueueParams{WatchName: "incoming", Path: "/drop/crash"}); err != nil {
		t.Fatal(err)
	}
	if _, err := recoveredStore.Claim(ctx); err != nil {
		t.Fatal(err)
	}
	count, err := recoveredStore.RecoverRunning(ctx)
	if err != nil || count != 1 {
		t.Fatalf("recover = %d %v", count, err)
	}

	events, err := store.ListEvents(ctx, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	var failedEvent, interrupted Event
	for _, event := range events {
		switch {
		case event.JobID == failed.ID && event.Type == EventJobFailed:
			failedEvent = event
		case event.JobID == waiting.ID && event.Type == EventJobInterrupted:
			interrupted = event
		}
	}
	if failedEvent.Status != StatusFailed || failedEvent.Attempts != 1 {
		t.Fatalf("failed event = %+v", failedEvent)
	}
	if interrupted.Status != StatusQueued || interrupted.JobID != waiting.ID {
		t.Fatalf("interrupted event = %+v", interrupted)
	}
	recovered, err := recoveredStore.ListEvents(ctx, 0, 10)
	if err != nil || len(recovered) != 3 || recovered[2].Type != EventJobInterrupted || recovered[2].Status != StatusQueued {
		t.Fatalf("recovered events = %+v err=%v", recovered, err)
	}
}

func TestEventSequenceSurvivesRestartAndPrune(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, _, err := store.Enqueue(ctx, EnqueueParams{WatchName: "incoming", Path: filepath.Join("/drop", string(rune('a'+i)))}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pruned, err := store.PruneEvents(ctx, 2)
	if err != nil || pruned != 3 {
		t.Fatalf("pruned = %d err=%v", pruned, err)
	}
	remaining, err := store.ListEvents(ctx, 0, 10)
	if err != nil || len(remaining) != 2 || remaining[0].Seq != 4 || remaining[1].Seq != 5 {
		t.Fatalf("remaining = %+v err=%v", remaining, err)
	}
	if _, _, err := store.Enqueue(ctx, EnqueueParams{WatchName: "incoming", Path: "/drop/next"}); err != nil {
		t.Fatal(err)
	}
	next, err := store.ListEvents(ctx, 5, 10)
	if err != nil || len(next) != 1 || next[0].Seq != 6 {
		t.Fatalf("next = %+v err=%v", next, err)
	}
	if pruned, err = store.PruneEvents(ctx, 0); err != nil || pruned != 0 {
		t.Fatalf("default retention pruned %d events: %v", pruned, err)
	}
}
