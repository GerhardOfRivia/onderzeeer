package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/GerhardOfRivia/onderzeeer/internal/queue"
)

func TestJobsFilterAndShowExternalID(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	configPath := filepath.Join(directory, "onderzeeer.yaml")
	databasePath := filepath.Join(directory, "onderzeeer.db")
	watchPath := filepath.Join(directory, "incoming")
	if err := os.Mkdir(watchPath, 0o700); err != nil {
		t.Fatal(err)
	}
	configuration := fmt.Sprintf(`
queue: {workers: 1, max_retries: 0}
database: {path: %s}
watches:
  - name: incoming
    path: %s
    pipeline: [{name: inspect, program: /bin/true}]
`, strconv.Quote(databasePath), strconv.Quote(watchPath))
	if err := os.WriteFile(configPath, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := queue.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := store.Enqueue(context.Background(), queue.EnqueueParams{
		WatchName: "incoming", Path: filepath.Join(watchPath, "tagged.csv"), ExternalID: "batch-42",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Enqueue(context.Background(), queue.EnqueueParams{
		WatchName: "incoming", Path: filepath.Join(watchPath, "plain.csv"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"jobs", configPath, "--external-id", "batch-42", "--local"}, &stdout, &stderr); code != 0 {
		t.Fatalf("jobs filter: %d %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "tagged.csv") || strings.Contains(stdout.String(), "plain.csv") {
		t.Fatalf("filtered jobs:\n%s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"job", configPath, fmt.Sprint(job.ID), "--local"}, &stdout, &stderr); code != 0 {
		t.Fatalf("job detail: %d %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "External ID:") || !strings.Contains(stdout.String(), "batch-42") {
		t.Fatalf("job detail:\n%s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"jobs", configPath, "--external-id", strings.Repeat("x", queue.MaxExternalIDBytes+1), "--local"}, &stdout, &stderr); code == 0 {
		t.Fatal("oversized external id accepted")
	}
}
