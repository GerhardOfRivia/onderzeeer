package control

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GerhardOfRivia/onderzeeer/internal/config"
	"github.com/GerhardOfRivia/onderzeeer/internal/queue"
)

func TestPurgeInactiveQueuesAndRestore(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	paths := []string{registeredConfig(t, root, "active"), registeredConfig(t, root, "stopped"), registeredConfig(t, root, "failed")}
	manager, client, socket := newUnixTransportHarness(t, Options{StateDirectory: state, Runner: func(ctx context.Context, cfg *config.Config, logger *slog.Logger) error {
		if cfg.Watches[0].Name == "failed" {
			return errors.New("runner failed")
		}
		return waitingRunner(ctx, cfg, logger)
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	instances, err := client.Start(ctx, paths, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, instance := range instances {
		store, err := queue.Open(instance.DatabasePath)
		if err != nil {
			t.Fatal(err)
		}
		_, _, enqueueErr := store.Enqueue(ctx, queue.EnqueueParams{WatchName: "incoming", Path: filepath.Join(root, "source.txt")})
		if err := errors.Join(enqueueErr, store.Close()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.Stop(ctx, "stopped"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Wait(ctx, "failed"); err != nil {
		t.Fatal(err)
	}
	// A failed queue still desires running and would otherwise restore.
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if err := os.WriteFile(instances[2].DatabasePath+suffix, []byte("sidecar"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(root, "source.txt")
	if err := os.WriteFile(source, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := client.System(ctx)
	if err != nil || info.StateDirectory != state || info.SocketPath != socket || info.RegistryPath != filepath.Join(state, "registry.sqlite") || info.ActiveQueues != 1 || info.InactiveQueues != 2 || info.PID != os.Getpid() || info.StartedAt.IsZero() {
		t.Fatalf("system info = %+v, %v", info, err)
	}
	result, err := client.PurgeInactive(ctx)
	if err != nil || len(result.Removed) != 2 || len(result.Failures) != 0 {
		t.Fatalf("purge = %+v, %v", result, err)
	}
	if len(manager.KnownQueues()) != 1 || len(manager.List(true)) != 1 {
		t.Fatal("purged queues still in catalog")
	}
	for _, instance := range instances[1:] {
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			if _, err := os.Stat(instance.DatabasePath + suffix); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("purged file remains: %s%s: %v", instance.DatabasePath, suffix, err)
			}
		}
		if _, err := manager.Get(instance.Name); !errors.Is(err, ErrNotFound) {
			t.Fatalf("purged instance is still selectable: %v", err)
		}
	}
	for _, path := range append(paths, source, instances[0].DatabasePath) {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("purge removed preserved file %s: %v", path, err)
		}
	}
	result, err = client.PurgeInactive(ctx)
	if err != nil || len(result.Removed) != 0 || len(result.Failures) != 0 {
		t.Fatalf("repeat purge = %+v, %v", result, err)
	}
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	restored := durableManager(t, state, waitingRunner)
	if len(restored.List(true)) != 1 {
		t.Fatal("purged registrations returned after restart")
	}
	if count, err := restored.Restore(ctx); err != nil || count != 1 {
		t.Fatalf("restore = %d, %v", count, err)
	}
	// The purged name and configuration can be registered with a fresh queue.
	fresh, err := restored.StartMany([]string{paths[1]}, "stopped")
	if err != nil || fresh[0].ID == instances[1].ID {
		t.Fatalf("fresh registration = %+v, %v", fresh, err)
	}
}

func TestPurgeSkipsStoppingInstances(t *testing.T) {
	root := t.TempDir()
	release := make(chan struct{})
	cancelled := make(chan struct{})
	manager := durableManager(t, filepath.Join(root, "state"), func(ctx context.Context, _ *config.Config, _ *slog.Logger) error {
		<-ctx.Done()
		close(cancelled)
		<-release
		return ctx.Err()
	})
	instances, err := manager.StartMany([]string{registeredConfig(t, root, "worker")}, "")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := manager.Stop(context.Background(), instances[0].ID); done <- err }()
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("instance did not begin stopping")
	}
	result, err := manager.PurgeInactive(context.Background())
	if err != nil || len(result.Removed) != 0 || len(result.Failures) != 0 || len(manager.List(true)) != 1 {
		t.Fatalf("purged stopping instance: %+v, %v", result, err)
	}
}

func TestPurgeFailureRetainsRegistrationAndCanRetry(t *testing.T) {
	root := t.TempDir()
	manager := durableManager(t, filepath.Join(root, "state"), func(context.Context, *config.Config, *slog.Logger) error { return errors.New("failed") })
	instances, err := manager.StartMany([]string{registeredConfig(t, root, "worker")}, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := manager.Wait(ctx, instances[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(instances[0].DatabasePath, []byte("preserve on registry failure"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.registry.db.Exec(`CREATE TRIGGER reject_purge BEFORE DELETE ON instances BEGIN SELECT RAISE(ABORT, 'registry unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	result, err := manager.PurgeInactive(ctx)
	if err != nil || len(result.Removed) != 0 || len(result.Failures) != 1 {
		t.Fatalf("purge with registry failure = %+v, %v", result, err)
	}
	view, err := manager.Get(instances[0].ID)
	if err != nil || view.DesiredState != "stopped" {
		t.Fatalf("partial purge might restore: %+v, %v", view, err)
	}
	if _, err := os.Stat(view.DatabasePath); err != nil {
		t.Fatal("registry failure removed queue file", err)
	}
	if _, err := manager.registry.db.Exec("DROP TRIGGER reject_purge"); err != nil {
		t.Fatal(err)
	}
	result, err = manager.PurgeInactive(ctx)
	if err != nil || len(result.Removed) != 1 || len(result.Failures) != 0 {
		t.Fatalf("retry purge = %+v, %v", result, err)
	}
}

func TestPurgeDoesNotFollowQueueDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	manager := durableManager(t, state, waitingRunner)
	instances, err := manager.StartMany([]string{registeredConfig(t, root, "worker")}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Stop(context.Background(), instances[0].ID); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(state, "queues")
	if err := os.Rename(directory, directory+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, directory); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, instances[0].ID+".sqlite")
	if err := os.WriteFile(outside, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := manager.PurgeInactive(context.Background())
	if err != nil || len(result.Failures) != 1 || len(result.Removed) != 0 {
		t.Fatalf("symlink purge = %+v, %v", result, err)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "untouched" {
		t.Fatalf("purge followed symlink: %q, %v", data, err)
	}
}

func TestPurgeRacesWithRestart(t *testing.T) {
	root := t.TempDir()
	manager := durableManager(t, filepath.Join(root, "state"), waitingRunner)
	path := registeredConfig(t, root, "worker")
	for range 10 {
		instances, err := manager.StartMany([]string{path}, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Stop(context.Background(), instances[0].ID); err != nil {
			t.Fatal(err)
		}
		known := manager.KnownQueues()[0]
		start := make(chan struct{})
		restarted := make(chan error, 1)
		go func() {
			<-start
			_, err := manager.StartKnownQueueContext(context.Background(), known)
			restarted <- err
		}()
		close(start)
		result, err := manager.PurgeInactive(context.Background())
		if err != nil || len(result.Failures) != 0 {
			t.Fatalf("purge during restart = %+v, %v", result, err)
		}
		restartErr := <-restarted
		if len(result.Removed) == 1 {
			if !errors.Is(restartErr, ErrNotFound) || len(manager.List(true)) != 0 {
				t.Fatalf("purged queue restarted: %v", restartErr)
			}
		} else {
			view, err := manager.Get(instances[0].ID)
			if restartErr != nil || err != nil || !view.Active() {
				t.Fatalf("restarted queue was purged: %+v, %v, %v", view, err, restartErr)
			}
			if _, err := manager.Stop(context.Background(), view.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestConfirmedPurgeKeepsQueuesOutsidePreviewAndChangedInstances(t *testing.T) {
	root := t.TempDir()
	paths := []string{
		registeredConfig(t, root, "reviewed"),
		registeredConfig(t, root, "later"),
		registeredConfig(t, root, "restarted"),
	}
	manager, client, _ := newUnixTransportHarness(t, Options{StateDirectory: filepath.Join(root, "state"), Runner: waitingRunner})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	instances, err := client.Start(ctx, paths, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, instance := range instances {
		if err := os.WriteFile(instance.DatabasePath, []byte("queue data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var preview []Instance
	for _, name := range []string{"reviewed", "restarted"} {
		instance, err := client.Stop(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		preview = append(preview, instance)
	}
	// An empty selection must never fall back to purging everything.
	result, err := client.PurgeSelected(ctx, nil)
	if err != nil || len(result.Removed) != 0 || len(manager.List(true)) != 3 {
		t.Fatalf("empty selected purge = %+v, %v", result, err)
	}
	// These lifecycle changes occur while the user is looking at the preview.
	if _, err := client.Stop(ctx, "later"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Start(ctx, []string{"restarted"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Stop(ctx, "restarted"); err != nil {
		t.Fatal(err)
	}
	result, err = client.PurgeSelected(ctx, preview)
	if err != nil || len(result.Removed) != 1 || result.Removed[0].Name != "reviewed" || len(result.Failures) != 1 || result.Failures[0].Name != "restarted" {
		t.Fatalf("confirmed purge = %+v, %v", result, err)
	}
	if _, err := manager.Get("reviewed"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reviewed queue was not removed: %v", err)
	}
	for _, name := range []string{"later", "restarted"} {
		instance, err := manager.Get(name)
		if err != nil {
			t.Fatal("unreviewed queue removed", err)
		}
		if data, err := os.ReadFile(instance.DatabasePath); err != nil || string(data) != "queue data" {
			t.Fatalf("unreviewed database changed: %q, %v", data, err)
		}
	}
	// A confirmed snapshot that is now actively running is also protected.
	fresh, err := client.Get(ctx, "later")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Start(ctx, []string{"later"}, ""); err != nil {
		t.Fatal(err)
	}
	result, err = client.PurgeSelected(ctx, []Instance{fresh})
	if err != nil || len(result.Removed) != 0 || len(result.Failures) != 1 {
		t.Fatalf("active selected purge = %+v, %v", result, err)
	}
}
