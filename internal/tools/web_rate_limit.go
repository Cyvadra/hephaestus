package tools

import (
	"context"
	"sync"
	"time"
)

const webProviderQPS = 2

type webProviderQueue struct {
	requests chan webProviderRequest
}

type webProviderRequest struct {
	ctx   context.Context
	ready chan error
}

type webProviderQueues struct {
	mu       sync.Mutex
	queues   map[string]*webProviderQueue
	interval time.Duration
}

func newWebProviderQueues() *webProviderQueues {
	return newWebProviderQueuesWithInterval(time.Second / webProviderQPS)

}

func newWebProviderQueuesWithInterval(interval time.Duration) *webProviderQueues {
	return &webProviderQueues{queues: make(map[string]*webProviderQueue), interval: interval}
}

func (q *webProviderQueues) wait(ctx context.Context, provider string) error {
	q.mu.Lock()
	queue := q.queues[provider]
	if queue == nil {
		queue = &webProviderQueue{requests: make(chan webProviderRequest, 64)}
		q.queues[provider] = queue
		go queue.run(q.interval)
	}
	q.mu.Unlock()

	request := webProviderRequest{ctx: ctx, ready: make(chan error, 1)}
	select {
	case queue.requests <- request:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-request.ready:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *webProviderQueue) run(interval time.Duration) {
	var next time.Time
	for request := range q.requests {
		if err := request.ctx.Err(); err != nil {
			request.ready <- err
			continue
		}
		if delay := time.Until(next); delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-request.ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				request.ready <- request.ctx.Err()
				continue
			}
		}
		next = time.Now().Add(interval)
		request.ready <- nil
	}
}

var sharedWebProviderQueues = newWebProviderQueues()
