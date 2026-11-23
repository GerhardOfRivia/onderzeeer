package queue

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPriorityIsClaimedBeforeOlderJobs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	store := openTestStore(t)
	store.now = func() time.Time { return now }
	low, _, err := store.Enqueue(ctx, EnqueueParams{WatchName: "incoming", Path: "/drop/low", Priority: 0})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	high, _, err := store.Enqueue(ctx, EnqueueParams{WatchName: "incoming", Path: "/drop/high", Priority: 5, Metadata: `{"source":"agent"}`})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(ctx)
	if err != nil || claimed.ID != high.ID || claimed.Priority != 5 || claimed.Metadata != `{"source":"agent"}` {
		t.Fatalf("claim = %+v %v", claimed, err)
	}
	claimed, err = store.Claim(ctx)
	if err != nil || claimed.ID != low.ID || claimed.Priority != 0 {
		t.Fatalf("second claim = %+v %v", claimed, err)
	}
}

func TestExternalIDIsUnique(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	first, created, err := store.Enqueue(ctx, EnqueueParams{WatchName: "incoming", Path: "/drop/a", ExternalID: "batch-7"})
	if err != nil || !created {
		t.Fatal(err)
	}
	existing, created, err := store.Enqueue(ctx, EnqueueParams{WatchName: "incoming", Path: "/drop/b", ExternalID: "batch-7"})
	if !errors.Is(err, ErrExternalIDInUse) || created || existing.ID != first.ID || existing.Path != "/drop/a" {
		t.Fatalf("conflict = %+v created=%v err=%v", existing, created, err)
	}
	if _, _, err := store.Enqueue(ctx, EnqueueParams{WatchName: "incoming", Path: "/drop/c", Priority: 2_000_000}); err == nil {
		t.Fatal("priority out of range accepted")
	}
	if _, _, err := store.Enqueue(ctx, EnqueueParams{WatchName: "incoming", Path: "/drop/d", Metadata: `["nope"]`}); err == nil {
		t.Fatal("metadata array accepted")
	}
}
