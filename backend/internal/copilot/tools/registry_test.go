package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"o1/internal/httpx"
	"o1/internal/incidents"
	"o1/internal/monitors"
	"o1/internal/workspaces"
)

// testNow is the fixed clock used by registry tests.
var testNow = time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)

type fakeIncidentSource struct {
	get      func(ctx context.Context, actorID, workspaceID, incidentID uuid.UUID) (incidents.IncidentDetail, error)
	list     func(ctx context.Context, actorID, workspaceID uuid.UUID, filter incidents.IncidentFilter, cursor *httpx.PageCursor, limit int) ([]incidents.Incident, error)
	outcomes func(ctx context.Context, actorID, workspaceID, incidentID uuid.UUID) ([]incidents.DeliveryOutcome, error)
}

func (f *fakeIncidentSource) Get(ctx context.Context, actorID, workspaceID, incidentID uuid.UUID) (incidents.IncidentDetail, error) {
	if f.get != nil {
		return f.get(ctx, actorID, workspaceID, incidentID)
	}
	return incidents.IncidentDetail{}, nil
}

func (f *fakeIncidentSource) List(ctx context.Context, actorID, workspaceID uuid.UUID, filter incidents.IncidentFilter, cursor *httpx.PageCursor, limit int) ([]incidents.Incident, error) {
	if f.list != nil {
		return f.list(ctx, actorID, workspaceID, filter, cursor, limit)
	}
	return []incidents.Incident{}, nil
}

func (f *fakeIncidentSource) DeliveryOutcomes(ctx context.Context, actorID, workspaceID, incidentID uuid.UUID) ([]incidents.DeliveryOutcome, error) {
	if f.outcomes != nil {
		return f.outcomes(ctx, actorID, workspaceID, incidentID)
	}
	return []incidents.DeliveryOutcome{}, nil
}

type fakeMonitorSource struct {
	get     func(ctx context.Context, actorID, workspaceID, monitorID uuid.UUID) (monitors.Monitor, error)
	list    func(ctx context.Context, actorID, workspaceID uuid.UUID, cursor *monitors.Cursor, limit int) ([]monitors.Monitor, error)
	results func(ctx context.Context, actorID, workspaceID, monitorID uuid.UUID, window time.Duration, cursor *monitors.Cursor, limit int) ([]monitors.ProbeResult, error)
	summary func(ctx context.Context, actorID, workspaceID, monitorID uuid.UUID, window time.Duration) (monitors.Summary, error)
}

func (f *fakeMonitorSource) Get(ctx context.Context, actorID, workspaceID, monitorID uuid.UUID) (monitors.Monitor, error) {
	if f.get != nil {
		return f.get(ctx, actorID, workspaceID, monitorID)
	}
	return monitors.Monitor{}, nil
}

func (f *fakeMonitorSource) List(ctx context.Context, actorID, workspaceID uuid.UUID, cursor *monitors.Cursor, limit int) ([]monitors.Monitor, error) {
	if f.list != nil {
		return f.list(ctx, actorID, workspaceID, cursor, limit)
	}
	return []monitors.Monitor{}, nil
}

func (f *fakeMonitorSource) Results(ctx context.Context, actorID, workspaceID, monitorID uuid.UUID, window time.Duration, cursor *monitors.Cursor, limit int) ([]monitors.ProbeResult, error) {
	if f.results != nil {
		return f.results(ctx, actorID, workspaceID, monitorID, window, cursor, limit)
	}
	return []monitors.ProbeResult{}, nil
}

func (f *fakeMonitorSource) Summary(ctx context.Context, actorID, workspaceID, monitorID uuid.UUID, window time.Duration) (monitors.Summary, error) {
	if f.summary != nil {
		return f.summary(ctx, actorID, workspaceID, monitorID, window)
	}
	return monitors.Summary{}, nil
}

func newTestRegistry(inc IncidentSource, mon MonitorSource, actorID uuid.UUID) *Registry {
	registry := NewRegistry(inc, mon, func() time.Time { return testNow })
	registry.actorFrom = func(context.Context) (uuid.UUID, bool) { return actorID, true }
	return registry
}

func TestSpecs_AreSixAndValidJSON(t *testing.T) {
	specs := Specs()
	if len(specs) != 6 {
		t.Fatalf("specs = %d, want 6", len(specs))
	}

	want := []string{
		"o1_list_monitors",
		"o1_get_monitor",
		"o1_list_incidents",
		"o1_get_incident",
		"o1_get_recent_checks",
		"o1_get_alert_deliveries",
	}
	for i, spec := range specs {
		if spec.Name != want[i] {
			t.Errorf("spec[%d] = %q, want %q", i, spec.Name, want[i])
		}
		if spec.Description == "" {
			t.Errorf("%s has no description", spec.Name)
		}

		var schema map[string]any
		if err := json.Unmarshal(spec.InputSchema, &schema); err != nil {
			t.Fatalf("%s: invalid schema: %v", spec.Name, err)
		}
		if schema["type"] != "object" {
			t.Errorf("%s: type = %v, want object", spec.Name, schema["type"])
		}
		if schema["additionalProperties"] != false {
			t.Errorf("%s: additionalProperties = %v, want false", spec.Name, schema["additionalProperties"])
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Errorf("%s: properties missing", spec.Name)
		}
		if _, ok := properties["workspace_id"]; ok {
			t.Errorf("%s accepts workspace_id; workspace comes from the caller", spec.Name)
		}

		switch spec.Name {
		case "o1_get_monitor", "o1_get_recent_checks":
			assertRequired(t, spec.Name, schema, "monitor_id")
		case "o1_get_incident", "o1_get_alert_deliveries":
			assertRequired(t, spec.Name, schema, "incident_id")
		}

		if spec.Name == "o1_get_alert_deliveries" && spec.MinRole != workspaces.RoleMember {
			t.Fatal("o1_get_alert_deliveries must be member-only")
		}
	}
}

func assertRequired(t *testing.T, name string, schema map[string]any, field string) {
	t.Helper()
	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("%s: required missing", name)
	}
	for _, value := range required {
		if value == field {
			return
		}
	}
	t.Fatalf("%s: %q is not required", name, field)
}

func TestCall_RejectsUnknownAndCrossTenant(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()

	t.Run("unknown tool", func(t *testing.T) {
		registry := newTestRegistry(nil, nil, uuid.New())
		if _, err := registry.Call(ctx, workspaceID, "o1_delete_everything", nil); !errors.Is(err, ErrUnknownTool) {
			t.Fatalf("err = %v, want ErrUnknownTool", err)
		}
	})

	t.Run("cross-tenant incident is not_found", func(t *testing.T) {
		incidentsSource := &fakeIncidentSource{
			get: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (incidents.IncidentDetail, error) {
				return incidents.IncidentDetail{}, incidents.ErrNotFound
			},
		}
		registry := newTestRegistry(incidentsSource, nil, uuid.New())
		args := json.RawMessage(`{"incident_id":"` + uuid.NewString() + `"}`)

		result, err := registry.Call(ctx, workspaceID, "o1_get_incident", args)
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		if !result.IsError || result.ErrorCode != "not_found" {
			t.Fatalf("result = %+v, want not_found", result)
		}
	})

	t.Run("viewer cannot read deliveries", func(t *testing.T) {
		viewerID := uuid.New()
		incidentsSource := &fakeIncidentSource{
			outcomes: func(_ context.Context, actorID, _, _ uuid.UUID) ([]incidents.DeliveryOutcome, error) {
				if actorID == viewerID {
					return nil, workspaces.ErrForbidden
				}
				return []incidents.DeliveryOutcome{}, nil
			},
		}
		registry := newTestRegistry(incidentsSource, nil, viewerID)
		args := json.RawMessage(`{"incident_id":"` + uuid.NewString() + `"}`)

		result, err := registry.Call(ctx, workspaceID, "o1_get_alert_deliveries", args)
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		if !result.IsError || result.ErrorCode != "forbidden" {
			t.Fatalf("result = %+v, want forbidden", result)
		}
	})

	t.Run("bad identifier is invalid_argument", func(t *testing.T) {
		registry := newTestRegistry(&fakeIncidentSource{}, nil, uuid.New())
		result, err := registry.Call(ctx, workspaceID, "o1_get_incident", json.RawMessage(`{"incident_id":"not-a-uuid"}`))
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		if !result.IsError || result.ErrorCode != "invalid_argument" {
			t.Fatalf("result = %+v, want invalid_argument", result)
		}
	})
}

func TestCall_TruncatesStructurally(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	monitorID := uuid.New()

	checks := make([]monitors.ProbeResult, 100)
	for i := range checks {
		code := 500
		checks[i] = monitors.ProbeResult{
			ID:         uuid.New(),
			CheckedAt:  testNow.Add(-time.Duration(i) * time.Minute),
			Status:     "down",
			StatusCode: &code,
			LatencyMS:  100 + i,
			Error:      stringPointer("timeout: dial tcp 10.0.0.1:443"),
		}
	}
	monitorsSource := &fakeMonitorSource{
		results: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, time.Duration, *monitors.Cursor, int) ([]monitors.ProbeResult, error) {
			return checks, nil
		},
		summary: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, time.Duration) (monitors.Summary, error) {
			return monitors.Summary{Checks: 100, Down: 100, P95LatencyMS: 250}, nil
		},
	}
	registry := newTestRegistry(nil, monitorsSource, uuid.New())
	args := json.RawMessage(`{"monitor_id":"` + monitorID.String() + `","hours":24}`)

	result, err := registry.Call(ctx, workspaceID, "o1_get_recent_checks", args)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if result.IsError {
		t.Fatalf("result is an error: %+v", result)
	}

	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if len(raw) > maxResultBytes {
		t.Fatalf("result = %d bytes, want <= %d", len(raw), maxResultBytes)
	}
	if !result.Meta.Truncated {
		t.Fatal("Meta.Truncated = false, want true")
	}
	if result.Meta.Count != 100 {
		t.Fatalf("Meta.Count = %d, want 100", result.Meta.Count)
	}
	if result.Meta.Total != 100 {
		t.Fatalf("Meta.Total = %d, want 100", result.Meta.Total)
	}
	if result.Meta.Returned >= 100 || result.Meta.Returned <= 0 {
		t.Fatalf("Meta.Returned = %d, want a positive truncated value", result.Meta.Returned)
	}
	if got := result.Meta.AsOf; !got.Equal(testNow) {
		t.Fatalf("Meta.AsOf = %s, want %s", got, testNow)
	}

	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	value, ok := resolvePointer(document, "/data/checks/0/status")
	if !ok {
		t.Fatal("/data/checks/0/status did not resolve after truncation")
	}
	if value != "down" {
		t.Fatalf("/data/checks/0/status = %v, want down", value)
	}

	last := result.Meta.Returned - 1
	if _, ok := resolvePointer(document, "/data/checks/"+strconv.Itoa(last)+"/status"); !ok {
		t.Fatalf("/data/checks/%d/status did not resolve for a returned item", last)
	}
	if _, ok := resolvePointer(document, "/data/checks/"+strconv.Itoa(result.Meta.Returned)+"/status"); ok {
		t.Fatalf("/data/checks/%d/status resolved although it was dropped", result.Meta.Returned)
	}
}

func TestCall_ListMonitorsFillsActiveIncident(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	downID := uuid.New()
	upID := uuid.New()
	activeIncidentID := uuid.New()

	monitorsSource := &fakeMonitorSource{
		list: func(context.Context, uuid.UUID, uuid.UUID, *monitors.Cursor, int) ([]monitors.Monitor, error) {
			return []monitors.Monitor{
				{ID: downID, Name: "down", URL: "https://secret.example.com", Headers: map[string]string{"Authorization": "Bearer secret"}},
				{ID: upID, Name: "up"},
			}, nil
		},
	}
	incidentsSource := &fakeIncidentSource{
		list: func(context.Context, uuid.UUID, uuid.UUID, incidents.IncidentFilter, *httpx.PageCursor, int) ([]incidents.Incident, error) {
			return []incidents.Incident{{ID: activeIncidentID, MonitorID: downID}}, nil
		},
	}
	registry := newTestRegistry(incidentsSource, monitorsSource, uuid.New())

	result, err := registry.Call(ctx, workspaceID, "o1_list_monitors", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, secret := range []string{"secret.example.com", "Authorization", "Bearer"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("leaked %q: %s", secret, raw)
		}
	}

	data, ok := result.Data.(*monitorListData)
	if !ok {
		t.Fatalf("data = %T, want *monitorListData", result.Data)
	}
	if len(data.Monitors) != 2 {
		t.Fatalf("monitors = %d, want 2", len(data.Monitors))
	}
	if data.Monitors[0].ActiveIncidentID == nil || *data.Monitors[0].ActiveIncidentID != activeIncidentID {
		t.Fatalf("active incident = %v, want %s", data.Monitors[0].ActiveIncidentID, activeIncidentID)
	}
	if data.Monitors[1].ActiveIncidentID != nil {
		t.Fatalf("up monitor active incident = %v, want nil", data.Monitors[1].ActiveIncidentID)
	}
}

func stringPointer(value string) *string { return &value }

// resolvePointer follows an RFC 6901 JSON Pointer through a document decoded
// with encoding/json.
func resolvePointer(document any, pointer string) (any, bool) {
	if pointer == "" {
		return document, true
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	current := document
	for _, token := range strings.Split(pointer[1:], "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		switch node := current.(type) {
		case map[string]any:
			value, ok := node[token]
			if !ok {
				return nil, false
			}
			current = value
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(node) {
				return nil, false
			}
			current = node[index]
		default:
			return nil, false
		}
	}
	return current, true
}
