package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/riverqueue/river"

	"o1/internal/copilot"
	"o1/internal/jobs"
	"o1/internal/observability"
)

// defaultInvestigationJobTimeout bounds one attempt when the caller does not
// supply a budget. The runner has its own shorter wall clock, so this only
// protects the worker from a stuck database or provider transport.
const defaultInvestigationJobTimeout = 2 * time.Minute

// InvestigationWorker runs one copilot investigation per job. The service
// owns all persistence and retry semantics; the worker only maps the job fields
// onto Run and stays a no-op when the copilot is disabled.
type InvestigationWorker struct {
	river.WorkerDefaults[jobs.InvestigationArgs]

	svc     *copilot.Service
	logger  *slog.Logger
	timeout time.Duration
}

// NewInvestigationWorker builds a worker. svc may be nil when the copilot is
// off; logger and metrics may be nil. The service records the investigation
// metrics itself, so the worker only keeps the metrics argument for wiring
// symmetry with the other workers. A zero timeout uses the two-minute default;
// the worker binary passes the configured run deadline plus a margin.
func NewInvestigationWorker(svc *copilot.Service, logger *slog.Logger, _ *observability.Metrics, timeout time.Duration) *InvestigationWorker {
	if logger == nil {
		logger = slog.Default()
	}
	if timeout <= 0 {
		timeout = defaultInvestigationJobTimeout
	}
	return &InvestigationWorker{svc: svc, logger: logger, timeout: timeout}
}

// Timeout bounds each job run.
func (w *InvestigationWorker) Timeout(*river.Job[jobs.InvestigationArgs]) time.Duration {
	return w.timeout
}

// Work runs one investigation attempt. A disabled service ignores the job so a
// queued row left behind by a configuration change cannot tie up a worker.
func (w *InvestigationWorker) Work(ctx context.Context, job *river.Job[jobs.InvestigationArgs]) error {
	if w.svc == nil || !w.svc.Enabled() {
		w.logger.Log(ctx, slog.LevelDebug, "investigation skipped: copilot disabled",
			"investigation_id", job.Args.InvestigationID)
		return nil
	}
	return w.svc.Run(ctx, job.Args.InvestigationID, job.Attempt, job.MaxAttempts)
}
