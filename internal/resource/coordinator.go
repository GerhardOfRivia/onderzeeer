// Package resource coordinates in-process, exclusive named reservations.
package resource

import (
	"container/list"
	"context"
	"sync"
)

// Coordinator grants each name to one caller at a time in FIFO request order.
// Its zero value is ready to use. Names are configuration literals; declarations
// do not own reservations. Idle names are discarded only after the last release.
type Coordinator struct {
	mu     sync.Mutex
	queues map[string]*list.List
}

type request struct {
	ready chan struct{}
	ctx   context.Context
}

// Acquire waits without holding the coordinator mutex. The returned release
// function must be called after execution and is safe to call more than once.
// Cancellation removes even a concurrently granted request before returning.
func (c *Coordinator) Acquire(ctx context.Context, name string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.queues == nil {
		c.queues = make(map[string]*list.List)
	}
	q := c.queues[name]
	if q == nil {
		q = list.New()
		c.queues[name] = q
	}
	r := &request{ready: make(chan struct{}), ctx: ctx}
	element := q.PushBack(r)
	if q.Front() == element {
		close(r.ready)
	}
	c.mu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			front := q.Front() == element
			q.Remove(element)
			if front {
				// Skip canceled waiters immediately rather than depending on
				// their goroutines being scheduled to pass on the reservation.
				for q.Front() != nil {
					next := q.Front().Value.(*request)
					if next.ctx.Err() == nil {
						close(next.ready)
						break
					}
					q.Remove(q.Front())
				}
			}
			if q.Len() == 0 && c.queues[name] == q {
				delete(c.queues, name)
			}
		})
	}
	select {
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	case <-r.ready:
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		return release, nil
	}
}
