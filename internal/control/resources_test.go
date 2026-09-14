package control

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GerhardOfRivia/onderzeeer/internal/queue"
)

// The real local executor launches this helper. A marker proves execution has
// begun; a release file allows normal completion without timing-based sleeps.
func TestResourceInstanceHelper(t *testing.T) {
	root := os.Getenv("ONDERZEEER_RESOURCE_HELPER_ROOT")
	if root == "" {
		return
	}
	name := os.Getenv("ONDERZEEER_RESOURCE_HELPER_NAME")
	if err := os.WriteFile(filepath.Join(root, name+".entered"), []byte("entered"), 0600); err != nil {
		os.Exit(2)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, name+".release")); err == nil {
			os.Exit(0)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func resourceInstanceConfig(t *testing.T, root, name, resource string) string {
	t.Helper()
	incoming := filepath.Join(root, name+"-incoming")
	if err := os.Mkdir(incoming, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incoming, "input"), []byte(name), 0600); err != nil {
		t.Fatal(err)
	}
	declaration, reference := "", ""
	if resource != "" {
		declaration = fmt.Sprintf("resources: [%q]\n", resource)
		reference = fmt.Sprintf("        resources: %q\n", resource)
	}
	contents := fmt.Sprintf(`%squeue: {workers: 1, max_retries: 0}
watches:
  - name: incoming
    path: %q
    process_existing: true
    settle_for: 0s
    pipeline:
      - name: hold
%s        program: %q
        args: ['-test.run=^TestResourceInstanceHelper$']
        env:
          ONDERZEEER_RESOURCE_HELPER_ROOT: %q
          ONDERZEEER_RESOURCE_HELPER_NAME: %q
`, declaration, incoming, reference, os.Args[0], root, name)
	path := filepath.Join(root, name+".yaml")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitResourceCondition(t *testing.T, description string, f func() bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for !f() {
		select {
		case <-deadline:
			t.Fatal("timed out: " + description)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func resourceMarker(root, name string) bool {
	_, err := os.Stat(filepath.Join(root, name+".entered"))
	return err == nil
}

func TestRegisteredInstancesShareResourcesAndRestoreIndependentQueues(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	alpha := resourceInstanceConfig(t, root, "alpha", "gpu")
	beta := resourceInstanceConfig(t, root, "beta", "gpu")
	disk := resourceInstanceConfig(t, root, "disk", "disk")
	free := resourceInstanceConfig(t, root, "free", "")
	manager := durableManager(t, state, nil)
	first, err := manager.StartMany([]string{alpha}, "")
	if err != nil {
		t.Fatal(err)
	}
	waitResourceCondition(t, "alpha executing", func() bool { return resourceMarker(root, "alpha") })
	others, err := manager.StartMany([]string{beta, disk, free}, "")
	if err != nil {
		t.Fatal(err)
	}
	// Read the retained stream under its own lock; lifecycle operations remain
	// usable while another instance is waiting for the reservation.
	waitResourceCondition(t, "beta waiting", func() bool {
		manager.mu.Lock()
		logs := manager.instances[others[0].ID].logs
		manager.mu.Unlock()
		return strings.Contains(strings.Join(logs.Snapshot(), "\n"), "waiting for resource")
	})
	waitResourceCondition(t, "independent resources", func() bool { return resourceMarker(root, "disk") && resourceMarker(root, "free") })
	if resourceMarker(root, "beta") {
		t.Fatal("gpu commands overlapped across instances")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := manager.Stop(ctx, others[0].ID); err != nil {
		t.Fatal(err)
	}
	if len(manager.List(false)) != 3 {
		t.Fatal("waiting instance blocked unrelated lifecycle operations")
	}
	if _, err := manager.StartMany([]string{others[0].ID}, ""); err != nil {
		t.Fatal(err)
	}
	if resourceMarker(root, "beta") {
		t.Fatal("stopping a waiter released another instance's reservation")
	}
	// Cancellation of the holder releases its scheduler reservation.
	if _, err := manager.Stop(ctx, first[0].ID); err != nil {
		t.Fatal(err)
	}
	waitResourceCondition(t, "beta acquired after holder stopped", func() bool { return resourceMarker(root, "beta") })
	for _, name := range []string{"beta", "disk", "free"} {
		if err := os.WriteFile(filepath.Join(root, name+".release"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, instance := range others {
		store, err := queue.OpenReadOnly(instance.DatabasePath)
		if err != nil {
			t.Fatal(err)
		}
		waitResourceCondition(t, "job succeeded", func() bool {
			count, err := store.Count(context.Background(), queue.StatusSucceeded)
			return err == nil && count == 1
		})
		jobs, err := store.ListJobs(context.Background(), queue.JobFilter{})
		if err != nil || len(jobs) != 1 {
			t.Fatalf("instance queue mixed: %+v, %v", jobs, err)
		}
		runs, err := store.ListRuns(context.Background(), jobs[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		wantRuns := 1
		if instance.ID == others[0].ID {
			wantRuns = 2
		}
		if len(runs) != wantRuns {
			t.Fatalf("history not independent: %+v", runs)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{alpha, beta, disk, free} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	restored := durableManager(t, state, nil)
	count, err := restored.Restore(context.Background())
	if err != nil || count != 3 {
		t.Fatalf("Restore: %d, %v", count, err)
	}
	for _, instance := range append(first, others...) {
		view, err := restored.Get(instance.ID)
		if err != nil {
			t.Fatal(err)
		}
		if view.ConfigHash != instance.ConfigHash || view.DatabasePath != instance.DatabasePath {
			t.Fatalf("snapshot identity changed: %+v", view)
		}
	}
	restored.mu.Lock()
	snapshot := restored.instances[others[0].ID].config
	restored.mu.Unlock()
	if len(snapshot.Resources) != 1 || snapshot.Resources[0] != "gpu" || snapshot.Watches[0].Pipeline[0].Resources != "gpu" {
		t.Fatalf("restored resources lost: %+v", snapshot)
	}
	// Restore another GPU user from its stopped snapshot and enqueue new work
	// for both queues to prove the restored runners still share one coordinator.
	if _, err := restored.StartMany([]string{first[0].ID}, ""); err != nil {
		t.Fatal(err)
	}
	// Alpha's canceled execution had zero retries, so discover a new file.
	if err := os.WriteFile(filepath.Join(root, "alpha-incoming", "second"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitResourceCondition(t, "restored alpha acquired", func() bool {
		restored.mu.Lock()
		logs := restored.instances[first[0].ID].logs
		restored.mu.Unlock()
		return strings.Contains(strings.Join(logs.Snapshot(), "\n"), "resource acquired")
	})
	if err := os.Remove(filepath.Join(root, "beta.release")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "beta-incoming", "second"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitResourceCondition(t, "restored beta waiting", func() bool {
		restored.mu.Lock()
		logs := restored.instances[others[0].ID].logs
		restored.mu.Unlock()
		return strings.Contains(strings.Join(logs.Snapshot(), "\n"), "waiting for resource")
	})
	restored.mu.Lock()
	logs := restored.instances[others[0].ID].logs
	restored.mu.Unlock()
	if strings.Contains(strings.Join(logs.Snapshot(), "\n"), "running command") {
		t.Fatal("restored instances did not coordinate")
	}
}
