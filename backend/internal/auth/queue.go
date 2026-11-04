package auth

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const (
	// passwordResetQueueCapacity bounds the number of pending reset jobs.
	passwordResetQueueCapacity = 64
	// passwordResetWorkers is the fixed number of reset workers per service.
	passwordResetWorkers = 2
	// passwordResetJobTimeout bounds one background reset attempt.
	passwordResetJobTimeout = 30 * time.Second
	// passwordResetDrainTimeout bounds how long Close waits for workers.
	passwordResetDrainTimeout = 10 * time.Second
)

// passwordResetJob carries a detached context and the requested email.
type passwordResetJob struct {
	ctx   context.Context
	email string
}

// passwordResetQueue is a bounded, fixed-concurrency in-process queue. Workers
// start lazily on the first enqueue so services that never queue resets do not
// leave goroutines behind.
type passwordResetQueue struct {
	mu      sync.Mutex
	jobs    chan passwordResetJob
	workers int
	started bool
	closed  bool
	wg      sync.WaitGroup
	run     func(context.Context, string)
}

func newPasswordResetQueue(workers, capacity int, run func(context.Context, string)) *passwordResetQueue {
	return &passwordResetQueue{
		jobs:    make(chan passwordResetJob, capacity),
		workers: workers,
		run:     run,
	}
}

// enqueue performs a non-blocking enqueue. It returns false when the queue is
// full or has been closed.
func (q *passwordResetQueue) enqueue(job passwordResetJob) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	if !q.started {
		q.started = true
		for i := 0; i < q.workers; i++ {
			q.wg.Add(1)
			go q.worker()
		}
	}
	select {
	case q.jobs <- job:
		return true
	default:
		return false
	}
}

func (q *passwordResetQueue) worker() {
	defer q.wg.Done()
	for job := range q.jobs {
		q.run(job.ctx, job.email)
	}
}

// close stops accepting jobs, drains accepted work, and waits for workers
// using the default drain timeout.
func (q *passwordResetQueue) close() {
	q.closeWithTimeout(passwordResetDrainTimeout)
}

// closeWithTimeout stops accepting jobs and waits up to d for workers to
// finish. If they are still stuck in a send, it logs drain_timeout and returns
// instead of blocking shutdown. It is safe to call more than once.
func (q *passwordResetQueue) closeWithTimeout(d time.Duration) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	close(q.jobs)
	q.mu.Unlock()

	if d <= 0 {
		d = passwordResetDrainTimeout
	}
	drained := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(drained)
	}()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-drained:
	case <-timer.C:
		// A stuck sender must not hang process shutdown. No sensitive fields.
		slog.Warn("password reset queue drain timed out", "event", "drain_timeout")
	}
}

// QueuePasswordReset enqueues a password reset without waiting for the
// database lookup, token insert, or email delivery. If the bounded queue is
// full, the job is dropped and only a generic reason is logged.
func (s *Service) QueuePasswordReset(ctx context.Context, emailAddr string) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.resetQueue == nil {
		slog.Warn("password reset request dropped", "reason", "queue_unavailable")
		return
	}
	job := passwordResetJob{
		// The handler returns before this work runs; detach cancellation but
		// keep any values already on the context.
		ctx:   context.WithoutCancel(ctx),
		email: emailAddr,
	}
	if !s.resetQueue.enqueue(job) {
		slog.Warn("password reset request dropped", "reason", "queue_full")
	}
}

// runPasswordResetJob executes one queued reset with a detached, bounded
// context. Logs never include the email, token, URL, or raw sender error.
func (s *Service) runPasswordResetJob(base context.Context, emailAddr string) {
	ctx, cancel := context.WithTimeout(base, passwordResetJobTimeout)
	defer cancel()
	if err := s.RequestPasswordReset(ctx, emailAddr); err != nil {
		slog.Error("password reset job failed", "reason", "request_failed", "error_type", fmt.Sprintf("%T", err))
	}
}

// Close stops accepting queued password resets and waits for accepted jobs to
// finish. It is safe to call more than once.
func (s *Service) Close() {
	if s.resetQueue != nil {
		s.resetQueue.close()
	}
}
