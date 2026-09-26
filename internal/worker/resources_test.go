package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GerhardOfRivia/onderzeeer/internal/config"
	"github.com/GerhardOfRivia/onderzeeer/internal/executor"
	"github.com/GerhardOfRivia/onderzeeer/internal/queue"
	"github.com/GerhardOfRivia/onderzeeer/internal/resource"
)

type resourceExecutorFunc func(context.Context, executor.Command) (executor.Result, error)

func (f resourceExecutorFunc) Execute(ctx context.Context, command executor.Command) (executor.Result, error) {
	return f(ctx, command)
}

type resourceLog struct {
	mu      sync.Mutex
	text    strings.Builder
	waiting chan struct{}
	once    sync.Once
}

func (l *resourceLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.text.Write(p)
	if strings.Contains(string(p), "waiting for resource") {
		l.once.Do(func() { close(l.waiting) })
	}
	return len(p), nil
}
func (l *resourceLog) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.text.String() }

func resourcePool(t *testing.T, c *resource.Coordinator, store Store, run executor.Executor, steps ...config.CommandConfig) (*Pool, *resourceLog) {
	t.Helper()
	logs := &resourceLog{waiting: make(chan struct{})}
	p, err := New(store, NewConfigResolver([]config.WatchConfig{{Name: "incoming", Pipeline: steps}}), run,
		Options{Workers: 2, Resources: c, Logger: slog.New(slog.NewTextHandler(logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	return p, logs
}

func runResourceJob(p *Pool, ctx context.Context) <-chan error {
	done := make(chan error, 1)
	go func() {
		done <- p.processJob(ctx, &queue.Job{ID: 1, RunID: 1, WatchName: "incoming", Path: "/tmp/input"})
	}()
	return done
}

func resourceResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("resource job stuck")
		return nil
	}
}

func TestResourceWaitPrecedesHistoryAndTimeout(t *testing.T) {
	var c resource.Coordinator
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	release, err := c.Acquire(ctx, "gpu")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	store := &recordingStore{}
	// Exercise the real executor timeout after waiting longer than that timeout.
	p, logs := resourcePool(t, &c, store, executor.NewLocal(slog.New(slog.NewTextHandler(io.Discard, nil))),
		config.CommandConfig{Name: "test", Resources: "gpu", Program: "/bin/sh", Args: []string{"-c", "exit 0"}, Timeout: config.Duration{Duration: 100 * time.Millisecond}})
	done := runResourceJob(p, ctx)
	select {
	case <-logs.waiting:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-done:
		t.Fatalf("wait finished early: %v", err)
	case <-timer.C:
	}
	store.mu.Lock()
	if len(store.started) != 0 || store.failedRun != 0 {
		t.Error("waiting consumed an execution or failed attempt")
	}
	if len(store.resourceStates) != 1 || store.resourceStates[0] != queue.StatusPending {
		t.Errorf("waiting state = %v, want PENDING", store.resourceStates)
	}
	store.mu.Unlock()
	if strings.Contains(logs.String(), "running command") {
		t.Fatal("logged execution while waiting")
	}
	release()
	if err := resourceResult(t, done); err != nil {
		t.Fatal(err)
	}
	if len(store.resourceStates) != 2 || store.resourceStates[1] != queue.StatusRunning {
		t.Fatalf("acquired state = %v, want PENDING then RUNNING", store.resourceStates)
	}
	text := logs.String()
	previous := -1
	for _, event := range []string{"waiting for resource", "resource acquired", "running command", "resource released"} {
		i := strings.Index(text, event)
		if i <= previous {
			t.Fatalf("event order for %s: %s", event, text)
		}
		previous = i
	}
	if strings.Count(text, "resource released") != 1 {
		t.Fatal("duplicate release log")
	}
}

func TestCanceledResourceWaitDoesNotExecuteOrSpendRetries(t *testing.T) {
	var c resource.Coordinator
	holder, _ := c.Acquire(context.Background(), "gpu")
	defer holder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &recordingStore{}
	runner := &recordingExecutor{}
	p, logs := resourcePool(t, &c, store, runner, config.CommandConfig{Name: "test", Resources: "gpu", Program: "unused"})
	done := runResourceJob(p, ctx)
	select {
	case <-logs.waiting:
	case <-time.After(3 * time.Second):
		t.Fatal("not waiting")
	}
	cancel()
	if err := resourceResult(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("result = %v", err)
	}
	if len(store.started) != 0 || len(runner.names()) != 0 || store.failedRun != 0 || !store.interruptedWait {
		t.Fatalf("canceled wait consumed execution/retries: %+v", store)
	}
	holder()
	nextCtx, nextCancel := context.WithTimeout(context.Background(), time.Second)
	defer nextCancel()
	next, err := c.Acquire(nextCtx, "gpu")
	if err != nil {
		t.Fatal(err)
	}
	next()
}

func TestSharedPoolsAllowIndependentStepsAndReleaseBetweenSteps(t *testing.T) {
	var c resource.Coordinator
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered := make(chan string, 5)
	gate := make(chan struct{})
	run := resourceExecutorFunc(func(ctx context.Context, command executor.Command) (executor.Result, error) {
		entered <- command.Name
		select {
		case <-gate:
			return executor.Result{}, nil
		case <-ctx.Done():
			return executor.Result{}, ctx.Err()
		}
	})
	var dones []<-chan error
	for _, step := range []config.CommandConfig{
		{Name: "first", Resources: "gpu"}, {Name: "blocked", Resources: "gpu"},
		{Name: "disk", Resources: "disk"}, {Name: "free"},
	} {
		p, _ := resourcePool(t, &c, &recordingStore{}, run, step)
		dones = append(dones, runResourceJob(p, ctx))
		if step.Name == "first" {
			select {
			case name := <-entered:
				if name != "first" {
					t.Fatal(name)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case name := <-entered:
			if name == "blocked" {
				t.Fatal("same resource overlapped")
			}
			seen[name] = true
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if !seen["free"] || !seen["disk"] {
		t.Fatalf("independent steps didn't execute: %v", seen)
	}
	close(gate)
	for _, done := range dones {
		if err := resourceResult(t, done); err != nil {
			t.Fatal(err)
		}
	}

	// A step following a reserved step must find that reservation already free.
	p, _ := resourcePool(t, &c, &recordingStore{}, resourceExecutorFunc(func(ctx context.Context, command executor.Command) (executor.Result, error) {
		if command.Name == "second" {
			release, err := c.Acquire(ctx, "gpu")
			if err != nil {
				return executor.Result{}, err
			}
			release()
		}
		return executor.Result{}, nil
	}), config.CommandConfig{Name: "first", Resources: "gpu"}, config.CommandConfig{Name: "second"})
	if err := resourceResult(t, runResourceJob(p, ctx)); err != nil {
		t.Fatal(err)
	}
}

type resourceFailingStore struct {
	recordingStore
	startErr, completeErr, pendingErr, runningErr error
}

func (s *resourceFailingStore) MarkPending(ctx context.Context, jobID, runID int64) error {
	if s.pendingErr != nil {
		return s.pendingErr
	}
	return s.recordingStore.MarkPending(ctx, jobID, runID)
}
func (s *resourceFailingStore) MarkRunning(ctx context.Context, jobID, runID int64) error {
	if s.runningErr != nil {
		return s.runningErr
	}
	return s.recordingStore.MarkRunning(ctx, jobID, runID)
}

func (s *resourceFailingStore) StartCommand(ctx context.Context, start queue.CommandStart) (int64, error) {
	if s.startErr != nil {
		return 0, s.startErr
	}
	return s.recordingStore.StartCommand(ctx, start)
}
func (s *resourceFailingStore) CompleteCommand(ctx context.Context, id int64, result queue.CommandResult) error {
	if s.completeErr != nil {
		return s.completeErr
	}
	return s.recordingStore.CompleteCommand(ctx, id, result)
}

func TestReservationsReleasedOnEveryExecutionExit(t *testing.T) {
	for _, mode := range []string{"success", "failure", "timeout", "cancel", "pending persistence", "resume persistence", "start persistence", "result persistence", "panic"} {
		t.Run(mode, func(t *testing.T) {
			var c resource.Coordinator
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			store := &resourceFailingStore{}
			if mode == "pending persistence" {
				store.pendingErr = errors.New("pending failed")
			}
			if mode == "resume persistence" {
				store.runningErr = errors.New("resume failed")
			}
			if mode == "start persistence" {
				store.startErr = errors.New("start failed")
			}
			if mode == "result persistence" {
				store.completeErr = errors.New("complete failed")
			}
			run := resourceExecutorFunc(func(ctx context.Context, _ executor.Command) (executor.Result, error) {
				switch mode {
				case "failure":
					return executor.Result{ExitCode: 7}, errors.New("execution failed")
				case "timeout":
					return executor.Result{TimedOut: true}, executor.ErrTimeout
				case "cancel":
					cancel()
					return executor.Result{}, ctx.Err()
				case "panic":
					panic("executor panic")
				}
				return executor.Result{}, nil
			})
			p, _ := resourcePool(t, &c, store, run, config.CommandConfig{Name: "test", Resources: "gpu"})
			func() {
				defer func() {
					if value := recover(); value != nil && mode != "panic" {
						t.Fatal(value)
					}
				}()
				err := p.processJob(ctx, &queue.Job{ID: 1, RunID: 1, WatchName: "incoming"})
				if (err == nil) != (mode == "success") {
					t.Fatalf("result: %v", err)
				}
			}()
			nextCtx, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			next, err := c.Acquire(nextCtx, "gpu")
			if err != nil {
				t.Fatalf("reservation leaked: %v", err)
			}
			next()
		})
	}
}

func TestResourceWaitPersistsPendingBetweenPipelineSteps(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint("canceled=", canceled), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			store, err := queue.Open(filepath.Join(t.TempDir(), "queue.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if _, _, err := store.Enqueue(ctx, queue.EnqueueParams{WatchName: "incoming", Path: "/input"}); err != nil {
				t.Fatal(err)
			}
			job, err := store.Claim(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var c resource.Coordinator
			release, err := c.Acquire(ctx, "gpu")
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			p, logs := resourcePool(t, &c, store, resourceExecutorFunc(func(ctx context.Context, _ executor.Command) (executor.Result, error) {
				current, err := store.GetJob(ctx, job.ID)
				if err != nil || current.Status != queue.StatusRunning {
					return executor.Result{}, fmt.Errorf("execution state = %+v, %v", current, err)
				}
				return executor.Result{}, nil
			}), config.CommandConfig{Name: "prepare", Program: "unused"}, config.CommandConfig{Name: "locked", Program: "unused", Resources: "gpu"})
			done := make(chan error, 1)
			go func() { done <- p.processJob(ctx, job) }()
			select {
			case <-logs.waiting:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			current, err := store.GetJob(ctx, job.ID)
			if err != nil || current.Status != queue.StatusPending {
				t.Fatalf("waiting state = %+v, %v", current, err)
			}
			commands, err := store.ListCommands(ctx, job.RunID)
			if err != nil || len(commands) != 1 || commands[0].Status != queue.CommandSucceeded {
				t.Fatalf("waiting history = %+v, %v", commands, err)
			}
			want := queue.StatusSucceeded
			if canceled {
				cancel()
				want = queue.StatusQueued
			} else {
				release()
			}
			err = resourceResult(t, done)
			if canceled && !errors.Is(err, context.Canceled) || !canceled && err != nil {
				t.Fatalf("result = %v", err)
			}
			current, err = store.GetJob(context.Background(), job.ID)
			if err != nil || current.Status != want {
				t.Fatalf("final state = %+v, %v; want %s", current, err, want)
			}
		})
	}
}
