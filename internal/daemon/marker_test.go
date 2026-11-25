package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GerhardOfRivia/onderzeeer/internal/config"
	"github.com/GerhardOfRivia/onderzeeer/internal/queue"
)

func TestBezorgManifestStartsOneJob(t *testing.T) {
	directory := t.TempDir()
	incoming := filepath.Join(directory, "incoming")
	if err := os.Mkdir(incoming, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("raster")
	if err := os.WriteFile(filepath.Join(incoming, "scene.tif"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incoming, "scene.tif.bezorg-partial"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	manifestPath := filepath.Join(incoming, "scene.bezorg-done.json")
	body := `{
		"version": 1,
		"job_id": "bezorg-9",
		"external_id": "batch-7",
		"remote": "/outgoing/scene",
		"completed_at": "2026-11-23T12:00:00Z",
		"files": [{"path": "scene.tif", "size": 6, "sha256": "` + hex.EncodeToString(sum[:]) + `"}]
	}`
	if err := os.WriteFile(manifestPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	executed := filepath.Join(directory, "executed")
	database := filepath.Join(directory, "queue.db")
	cfg := markerConfig(database, incoming, config.MarkerBezorg, config.MarkerVerifySHA256, "*.bezorg-done.json", executed)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	defer stopDaemon(t, cancel, done)

	deadline := time.Now().Add(8 * time.Second)
	for {
		if contents, err := os.ReadFile(executed); err == nil {
			text := string(contents)
			if !strings.Contains(text, "batch-7") || !strings.Contains(text, "scene.tif") {
				t.Fatalf("pipeline arguments = %s", text)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("manifest was not processed")
		}
		select {
		case err := <-done:
			t.Fatalf("daemon exited: %v", err)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	store, err := queue.OpenReadOnly(database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	jobs, err := store.ListJobs(context.Background(), queue.JobFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ExternalID != "batch-7" || jobs[0].Path != manifestPath {
		t.Fatalf("jobs = %+v", jobs)
	}
}

func TestEmptyMarkerAndRejectedManifest(t *testing.T) {
	directory := t.TempDir()
	incoming := filepath.Join(directory, "incoming")
	if err := os.Mkdir(incoming, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incoming, "data.csv"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incoming, "_SUCCESS"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(directory, "queue.db")
	cfg := markerConfig(database, incoming, config.MarkerEmpty, config.MarkerVerifyExists, "_SUCCESS", filepath.Join(directory, "executed"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	job := waitForJob(t, done, database, func(job queue.Job) bool {
		return strings.HasSuffix(job.Path, "_SUCCESS") && job.ExternalID == ""
	})
	cancel()
	waitStop(t, done)
	if job.Path == "" {
		t.Fatal("empty marker did not enqueue")
	}

	rejectedDir := t.TempDir()
	badRoot := filepath.Join(rejectedDir, "incoming")
	if err := os.Mkdir(badRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	badManifest := filepath.Join(badRoot, "bad.bezorg-done.json")
	body := `{"version":1,"job_id":"j","external_id":"batch-9","remote":"/r","completed_at":"2026-11-23T12:00:00Z","files":[{"path":"missing.csv","size":1,"sha256":""}]}`
	if err := os.WriteFile(badManifest, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	badDB := filepath.Join(rejectedDir, "queue.db")
	cfg = markerConfig(badDB, badRoot, config.MarkerBezorg, config.MarkerVerifyExists, "*.bezorg-done.json", filepath.Join(rejectedDir, "executed"))
	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan error, 1)
	var logs strings.Builder
	go func() {
		done <- Run(ctx, cfg, slog.New(slog.NewTextHandler(&logs, nil)))
	}()
	deadline := time.Now().Add(8 * time.Second)
	for !strings.Contains(logs.String(), "rejected completion manifest") {
		if time.Now().After(deadline) {
			cancel()
			waitStop(t, done)
			t.Fatalf("manifest was not rejected: %s", logs.String())
		}
		select {
		case err := <-done:
			t.Fatalf("daemon exited after a rejected manifest: %v", err)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	waitStop(t, done)
	if _, err := os.Stat(badDB); err != nil {
		t.Fatal(err)
	}
	store, err := queue.OpenReadOnly(badDB)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	jobs, err := store.ListJobs(context.Background(), queue.JobFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("rejected manifest created jobs: %+v", jobs)
	}
}

func markerConfig(database, incoming string, format config.MarkerFormat, verify config.MarkerVerify, marker, executed string) *config.Config {
	return &config.Config{
		Queue:    config.QueueConfig{Workers: 1, RetryDelay: config.Duration{Duration: time.Millisecond}},
		Database: config.DatabaseConfig{Path: database},
		Watches: []config.WatchConfig{{
			Name: "incoming", Path: incoming, ProcessExisting: true,
			Trigger: config.TriggerMarker, Marker: marker, MarkerFormat: format, MarkerVerify: verify,
			SettleFor: config.Duration{Duration: 10 * time.Millisecond},
			Pipeline: []config.CommandConfig{{
				Name: "record", Program: os.Args[0],
				Args: []string{"-test.run=^TestDaemonHelperProcess$", "--", "{{external_id}}", "{{files}}"},
				Env:  map[string]string{"ONDERZEEER_DAEMON_TEST_MARKER": executed},
			}},
		}},
	}
}

func waitForJob(t *testing.T, done <-chan error, database string, match func(queue.Job) bool) queue.Job {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		store, err := queue.OpenReadOnly(database)
		if err == nil {
			jobs, listErr := store.ListJobs(context.Background(), queue.JobFilter{})
			closeErr := store.Close()
			if listErr == nil && closeErr == nil {
				for _, job := range jobs {
					if match(job) {
						return job
					}
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("job was not stored")
		}
		select {
		case err := <-done:
			t.Fatalf("daemon exited: %v", err)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func stopDaemon(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	waitStop(t, done)
}

func waitStop(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("daemon: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not stop")
	}
}
