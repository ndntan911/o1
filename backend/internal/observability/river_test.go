package observability

import (
	"context"
	"errors"
	"testing"

	"github.com/riverqueue/river/rivertype"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

func TestRiverMiddlewareSpan(t *testing.T) {
	tr, _ := testTracer(t, 1)
	mw := RiverMiddleware(nil, tr)

	job := &rivertype.JobRow{
		ID:      42,
		Kind:    "probe",
		Queue:   "probe",
		Attempt: 2,
	}

	var innerCalled bool
	err := mw.Work(context.Background(), job, func(ctx context.Context) error {
		innerCalled = true
		span := trace.SpanFromContext(ctx)
		if !span.SpanContext().IsValid() {
			t.Error("inner context has no valid span")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Work: %v", err)
	}
	if !innerCalled {
		t.Fatal("middleware did not call doInner")
	}

	spans := flushTracer(t, tr)
	if len(spans) != 1 {
		t.Fatalf("exported %d spans, want 1", len(spans))
	}
	got := spans[0]
	if got.Name != "river.probe" {
		t.Fatalf("span name = %q, want river.probe", got.Name)
	}
	for key, want := range map[string]string{
		"queue": "probe",
		"kind":  "probe",
	} {
		v, ok := attrValue(got, key)
		if !ok || v.AsString() != want {
			t.Fatalf("%s = %v (present %v), want %q (attrs: %v)", key, v, ok, want, got.Attributes)
		}
	}
	if v, ok := attrValue(got, "attempt"); !ok || v.AsInt64() != 2 {
		t.Fatalf("attempt = %v (present %v), want 2", v, ok)
	}
	if got.Status.Code != codes.Unset {
		t.Fatalf("status = %v for a successful job, want unset", got.Status)
	}
}

func TestRiverMiddlewareRecordsError(t *testing.T) {
	tr, _ := testTracer(t, 1)
	mw := RiverMiddleware(nil, tr)

	job := &rivertype.JobRow{Kind: "alert", Queue: "alert", Attempt: 1}
	wantErr := errors.New("delivery failed")
	err := mw.Work(context.Background(), job, func(context.Context) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Work error = %v, want %v", err, wantErr)
	}

	spans := flushTracer(t, tr)
	if len(spans) != 1 {
		t.Fatalf("exported %d spans, want 1", len(spans))
	}
	got := spans[0]
	if got.Name != "river.alert" {
		t.Fatalf("span name = %q, want river.alert", got.Name)
	}
	if got.Status.Code != codes.Error {
		t.Fatalf("status = %v, want error", got.Status.Code)
	}
	if got.Status.Description != wantErr.Error() {
		t.Fatalf("status description = %q, want %q", got.Status.Description, wantErr.Error())
	}
}

func TestRiverMiddlewareDisabledTracer(t *testing.T) {
	for name, mw := range map[string]rivertype.WorkerMiddleware{
		"nil tracer": RiverMiddleware(nil, nil),
		"disabled":   RiverMiddleware(nil, &Tracer{}),
	} {
		t.Run(name, func(t *testing.T) {
			called := false
			job := &rivertype.JobRow{Kind: "probe", Queue: "probe", Attempt: 1}
			if err := mw.Work(context.Background(), job, func(context.Context) error {
				called = true
				return nil
			}); err != nil {
				t.Fatalf("Work: %v", err)
			}
			if !called {
				t.Fatal("middleware did not call doInner")
			}
		})
	}
}
