package observability

import (
	"context"
	"time"

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
