package resource

import (
	"context"
	"errors"
	"testing"
	"time"
)

func waitQueue(t *testing.T, c *Coordinator, name string, length int) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		c.mu.Lock()
		got := 0
		if q := c.queues[name]; q != nil {
			got = q.Len()
		}
		c.mu.Unlock()
		if got == length {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("queue %s length = %d, want %d", name, got, length)
		case <-time.After(time.Millisecond):
		}
	}
}

func TestFIFOAndCanceledWaiter(t *testing.T) {
	var c Coordinator
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	holder, err := c.Acquire(ctx, "gpu")
	if err != nil {
		t.Fatal(err)
	}
	defer holder()
	type granted struct {
		id      int
		release func()
		err     error
	}
	results := make(chan granted, 4)
	var cancelWaiter context.CancelFunc
	for i := 0; i < 4; i++ {
		waitCtx := ctx
		if i == 1 {
			waitCtx, cancelWaiter = context.WithCancel(ctx)
		}
		go func(id int, ctx context.Context) {
			release, err := c.Acquire(ctx, "gpu")
			results <- granted{id, release, err}
		}(i, waitCtx)
		waitQueue(t, &c, "gpu", i+2)
	}
	cancelWaiter()
	r := <-results
	if r.id != 1 || !errors.Is(r.err, context.Canceled) || r.release != nil {
		t.Fatalf("cancel result: %+v", r)
	}
	waitQueue(t, &c, "gpu", 4)
	// Other resource names remain independent, including case differences.
	other, err := c.Acquire(ctx, "GPU")
	if err != nil {
		t.Fatal(err)
	}
	other()
	holder()
	holder() // Repeated release must not hand out a second reservation.
	for _, id := range []int{0, 2, 3} {
		r := <-results
		if r.err != nil || r.id != id {
			t.Fatalf("grant = %+v, want %d", r, id)
		}
		select {
		case extra := <-results:
			t.Fatalf("simultaneous grant: %+v", extra)
		default:
		}
		r.release()
	}
	waitQueue(t, &c, "gpu", 0)
}

func TestCancellationRacingHandoffDoesNotLeak(t *testing.T) {
	var c Coordinator
	for i := 0; i < 300; i++ {
		holder, err := c.Acquire(context.Background(), "gpu")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			release, err := c.Acquire(ctx, "gpu")
			if err == nil {
				release()
			}
		}()
		go cancel()
		holder()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("canceled acquisition stuck")
		}
		waitQueue(t, &c, "gpu", 0)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.queues) != 0 {
		t.Fatalf("leaked names: %v", c.queues)
	}
}
