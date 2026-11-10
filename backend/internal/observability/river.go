package observability

import (
	"context"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// riverMiddleware traces River job execution and leaves room for job duration
// metrics. It is safe with a nil metrics or tracer.
type riverMiddleware struct {
	river.MiddlewareDefaults

	metrics *Metrics
	tracer  *Tracer
}

// RiverMiddleware returns River worker middleware that starts a
// "river.<kind>" span per job with queue, attempt, and kind attributes. A job
// error is recorded and marks the span as failed.
func RiverMiddleware(metrics *Metrics, tracer *Tracer) rivertype.WorkerMiddleware {
	return &riverMiddleware{metrics: metrics, tracer: tracer}
}

// Work implements rivertype.WorkerMiddleware.
func (m *riverMiddleware) Work(ctx context.Context, job *rivertype.JobRow, doInner func(context.Context) error) error {
	if job == nil {
		return doInner(ctx)
	}

	ctx, span := m.tracer.Start(ctx, "river."+job.Kind, trace.WithAttributes(
		attribute.String("queue", job.Queue),
		attribute.Int("attempt", job.Attempt),
		attribute.String("kind", job.Kind),
	))
	defer span.End()

	start := time.Now()
	err := doInner(ctx)
	duration := time.Since(start)

	// The duration is recorded on the span today. A future job-duration metric
	// can consume the same measurement through m.metrics.
	span.SetAttributes(attribute.Int64("duration_ms", duration.Milliseconds()))

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}

// SentryErrorHandler captures River job errors and panics exactly once. It
// never cancels retries; River keeps applying the configured retry policy.
// Job args are deliberately not attached to the event.
type SentryErrorHandler struct{}

var _ river.ErrorHandler = SentryErrorHandler{}

// HandleError implements river.ErrorHandler.
func (SentryErrorHandler) HandleError(ctx context.Context, job *rivertype.JobRow, err error) *river.ErrorHandlerResult {
	if !shouldIgnore(err) {
		capture(ctx, err, jobTags(job), sentry.LevelError)
	}
	return &river.ErrorHandlerResult{}
}

// HandlePanic implements river.ErrorHandler.
func (SentryErrorHandler) HandlePanic(ctx context.Context, job *rivertype.JobRow, panicVal any, _ string) *river.ErrorHandlerResult {
	if panicVal != nil {
		capture(ctx, panicError(panicVal), jobTags(job), sentry.LevelFatal)
	}
	return &river.ErrorHandlerResult{}
}

// jobTags builds bounded Sentry tags from the job row. It never reads args.
func jobTags(job *rivertype.JobRow) map[string]string {
	if job == nil {
		return nil
	}
	return map[string]string{
		"kind":  job.Kind,
		"queue": job.Queue,
	}
}
