package queue

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestExternalIDIsStoredFilteredAndPreservedOnDuplicate(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()

	first, created, err := store.Enqueue(ctx, EnqueueParams{
		WatchName: "incoming", Path: "/drop/a.csv", Fingerprint: "v1", ExternalID: "  batch-7  ",
	})
	if err != nil || !created || first.ExternalID != "batch-7" {
		t.Fatalf("enqueue = %+v created=%v err=%v", first, created, err)
	}
	again, created, err := store.Enqueue(ctx, EnqueueParams{
		WatchName: "incoming", Path: "/drop/a.csv", Fingerprint: "v1", ExternalID: "other",
	})
	if err != nil || created || again.ID != first.ID || again.ExternalID != "batch-7" {
		t.Fatalf("duplicate = %+v created=%v err=%v", again, created, err)
	}
	other, created, err := store.Enqueue(ctx, EnqueueParams{
		WatchName: "incoming", Path: "/drop/b.csv", Fingerprint: "v1", ExternalID: "batch-7",
	})
	if !errors.Is(err, ErrExternalIDInUse) || created || other.ID != first.ID {
		t.Fatalf("second job = %+v created=%v err=%v", other, created, err)
	}
	plain, _, err := store.Enqueue(ctx, EnqueueParams{WatchName: "incoming", Path: "/drop/c.csv", Fingerprint: "v1"})
	if err != nil || plain.ExternalID != "" {
		t.Fatalf("plain job = %+v err=%v", plain, err)
	}

	matched, err := store.ListJobs(ctx, JobFilter{ExternalID: " batch-7 "})
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 || matched[0].ID != first.ID {
		t.Fatalf("filter = %+v", matched)
	}
	bySearch, err := store.ListJobs(ctx, JobFilter{Search: "BATCH-7"})
	if err != nil || len(bySearch) != 1 {
		t.Fatalf("search = %+v err=%v", bySearch, err)
	}
	none, err := store.ListJobs(ctx, JobFilter{ExternalID: "missing"})
	if err != nil || len(none) != 0 {
		t.Fatalf("missing filter = %+v err=%v", none, err)
	}

	loaded, err := store.GetJob(ctx, first.ID)
	if err != nil || loaded.ExternalID != "batch-7" {
		t.Fatalf("get = %+v err=%v", loaded, err)
	}
	if _, _, err := store.Enqueue(ctx, EnqueueParams{
		WatchName: "incoming", Path: "/drop/long.csv", ExternalID: strings.Repeat("a", MaxExternalIDBytes+1),
	}); err == nil {
		t.Fatal("oversized external id accepted")
	}
	if _, _, err := store.Enqueue(ctx, EnqueueParams{
		WatchName: "incoming", Path: "/drop/nul.csv", ExternalID: "bad\x00id",
	}); err == nil {
		t.Fatal("NUL external id accepted")
	}
}

func TestExternalIDMigrationKeepsExistingRows(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "queue.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(jobsSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO jobs (
		watch_name, path, fingerprint, status, attempts, max_retries,
		available_at, last_error, created_at, updated_at
	) VALUES ('incoming', '/old', 'v1', 'QUEUED', 0, 1, 1, '', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job, err := store.GetJob(context.Background(), 1)
	if err != nil || job.Path != "/old" || job.ExternalID != "" {
		t.Fatalf("upgraded job = %+v err=%v", job, err)
	}
	var index string
	if err := store.db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'jobs_external_id_idx'`).Scan(&index); err != nil || index != "jobs_external_id_idx" {
		t.Fatalf("external id index = %q err=%v", index, err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	if err := again.Close(); err != nil {
		t.Fatal(err)
	}
}
