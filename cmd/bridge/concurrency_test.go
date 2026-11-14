package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func waitQueued(t *testing.T, l *requestLimiter, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if l.snapshot().Queued == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queue did not reach %d: %+v", count, l.snapshot())
}
func TestAdmissionFIFOAndQueueBound(t *testing.T) {
	l := newRequestLimiter(1, 3, time.Second)
	if err := l.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	order := make(chan int, 3)
	for i := 0; i < 3; i++ {
		go func(i int) {
			if err := l.acquire(context.Background()); err != nil {
				order <- -1
				return
			}
			order <- i
			l.release()
		}(i)
		waitQueued(t, l, i+1)
	}
	if err := l.acquire(context.Background()); err == nil {
		t.Fatal("unbounded queue")
	} else if h, ok := err.(*httpError); !ok || h.status != 429 {
		t.Fatal(err)
	}
	l.release()
	for i := 0; i < 3; i++ {
		if got := <-order; got != i {
			t.Fatalf("FIFO: got %d, want %d", got, i)
		}
	}
}
func TestAdmissionCancellationAndTimeout(t *testing.T) {
	l := newRequestLimiter(1, 2, 30*time.Millisecond)
	_ = l.acquire(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- l.acquire(ctx) }()
	waitQueued(t, l, 1)
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if l.snapshot().Queued != 0 {
		t.Fatal("cancelled waiter retained")
	}
	if err := l.acquire(context.Background()); err == nil {
		t.Fatal("missing timeout")
	} else if h, ok := err.(*httpError); !ok || h.status != 503 {
		t.Fatal(err)
	}
	if s := l.snapshot(); s.Active != 1 || s.Queued != 0 {
		t.Fatal(s)
	}
	l.release()
}
func TestAdmissionCancelGrantRace(t *testing.T) {
	for i := 0; i < 100; i++ {
		l := newRequestLimiter(1, 1, time.Second)
		_ = l.acquire(context.Background())
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			if l.acquire(ctx) == nil {
				l.release()
			}
		}()
		waitQueued(t, l, 1)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); cancel() }()
		l.release()
		<-done
		wg.Wait()
		if s := l.snapshot(); s.Active != 0 || s.Queued != 0 {
			t.Fatal("leaked permit", s)
		}
	}
}

type countedBody struct{ reads atomic.Int64 }

func (b *countedBody) Read(p []byte) (int, error) { b.reads.Add(1); return 0, io.EOF }
func (b *countedBody) Close() error               { return nil }
func TestAdmissionDoesNotReadQueuedBody(t *testing.T) {
	old := admission
	admission = newRequestLimiter(1, 1, time.Second)
	defer func() { admission = old }()
	_ = admission.acquire(context.Background())
	defer admission.release()
	ctx, cancel := context.WithCancel(context.Background())
	body := &countedBody{}
	request := httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(ctx)
	request.Body = body
	done := make(chan struct{})
	go func() {
		defer close(done)
		withAdmission(func(w http.ResponseWriter, r *http.Request) { _, _ = io.ReadAll(r.Body) })(httptest.NewRecorder(), request)
	}()
	waitQueued(t, admission, 1)
	cancel()
	<-done
	if body.reads.Load() != 0 {
		t.Fatal("queued request body was read")
	}
}
