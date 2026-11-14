package main

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type requestWaiter struct {
	ready   chan struct{}
	granted bool
}
type requestLimiter struct {
	mu                         sync.Mutex
	limit, queueLimit, running int
	timeout                    time.Duration
	queue                      []*requestWaiter
}
type concurrencySnapshot struct {
	Active         int   `json:"active"`
	Queued         int   `json:"queued"`
	Limit          int   `json:"limit"`
	QueueLimit     int   `json:"queue_limit"`
	QueueTimeoutMS int64 `json:"queue_timeout_ms"`
}

// Includes account metadata operations that also protect credential homes.
var active atomic.Int64

var admission = newRequestLimiter(4, 16, 30*time.Second)

func newRequestLimiter(limit, queueLimit int, timeout time.Duration) *requestLimiter {
	return &requestLimiter{limit: limit, queueLimit: queueLimit, timeout: timeout}
}
func configureConcurrency() error {
	limit, err := strconv.Atoi(env("ACP_MAX_CONCURRENCY", "4"))
	if err != nil || limit < 1 {
		return fmt.Errorf("ACP_MAX_CONCURRENCY must be a positive integer")
	}
	queueLimit, err := strconv.Atoi(env("ACP_QUEUE_CAPACITY", "16"))
	if err != nil || queueLimit < 0 {
		return fmt.Errorf("ACP_QUEUE_CAPACITY must be a non-negative integer")
	}
	timeout, err := time.ParseDuration(env("ACP_QUEUE_TIMEOUT", "30s"))
	if err != nil || timeout <= 0 {
		return fmt.Errorf("ACP_QUEUE_TIMEOUT must be a positive duration")
	}
	admission = newRequestLimiter(limit, queueLimit, timeout)
	return nil
}
func (l *requestLimiter) acquire(ctx context.Context) error {
	l.mu.Lock()
	if err := ctx.Err(); err != nil {
		l.mu.Unlock()
		return err
	}
	if l.running < l.limit {
		l.running++
		l.mu.Unlock()
		return nil
	}
	if len(l.queue) >= l.queueLimit {
		l.mu.Unlock()
		return &httpError{429, "ACP waiting queue is full"}
	}
	waiter := &requestWaiter{ready: make(chan struct{})}
	l.queue = append(l.queue, waiter)
	l.mu.Unlock()
	timer := time.NewTimer(l.timeout)
	defer timer.Stop()
	select {
	case <-waiter.ready:
		if err := ctx.Err(); err != nil {
			l.release()
			return err
		}
		return nil
	case <-ctx.Done():
		l.withdraw(waiter)
		return ctx.Err()
	case <-timer.C:
		l.withdraw(waiter)
		return &httpError{503, "ACP waiting queue timed out"}
	}
}

// Cancellation and grant can race. Return an already-granted slot exactly once.
func (l *requestLimiter) withdraw(waiter *requestWaiter) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if waiter.granted {
		l.releaseLocked()
		return
	}
	for i, item := range l.queue {
		if item == waiter {
			copy(l.queue[i:], l.queue[i+1:])
			l.queue[len(l.queue)-1] = nil
			l.queue = l.queue[:len(l.queue)-1]
			return
		}
	}
}
func (l *requestLimiter) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.releaseLocked()
}
func (l *requestLimiter) releaseLocked() {
	if len(l.queue) == 0 {
		l.running--
		return
	}
	waiter := l.queue[0]
	l.queue[0] = nil
	l.queue = l.queue[1:]
	waiter.granted = true
	close(waiter.ready)
}
func (l *requestLimiter) snapshot() concurrencySnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	return concurrencySnapshot{Active: l.running, Queued: len(l.queue), Limit: l.limit, QueueLimit: l.queueLimit, QueueTimeoutMS: l.timeout.Milliseconds()}
}

// Admit before reading request bodies or preparing images. Waiting requests hold
// no parsed conversation and cancellation never starts an ACP process.
func withAdmission(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		err := admission.acquire(r.Context())
		if record := requestTrace(r.Context()); record != nil {
			record.QueueMS = float64(time.Since(start).Microseconds()) / 1000
			if err == nil {
				record.Status = "running"
				record.SessionReason = "not_matched"
			}
			publishRequestTrace(record)
		}
		if err != nil {
			if r.Context().Err() == nil {
				w.Header().Set("Retry-After", "1")
				sendError(w, err)
			}
			return
		}
		active.Add(1)
		defer active.Add(-1)
		defer admission.release()
		next(w, r)
	}
}
