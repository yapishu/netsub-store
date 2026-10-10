package store

import (
	"context"
	"sync"
	"time"
)

// requestPacer spaces provider calls across all orders and quote requests.
// Waiting callers reserve no future slots, so cancellation cannot build a backlog.
type requestPacer struct {
	mu   sync.Mutex
	next time.Time
}

func (p *requestPacer) wait(ctx context.Context, interval time.Duration) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		p.mu.Lock()
		now := time.Now()
		delay := p.next.Sub(now)
		if delay <= 0 {
			p.next = now.Add(interval)
			p.mu.Unlock()
			return nil
		}
		p.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (p *requestPacer) deferFor(delay time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	until := time.Now().Add(delay)
	if until.After(p.next) {
		p.next = until
	}
}
