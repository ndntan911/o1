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

	"o1/internal/auth"
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

func newTestRegistry(inc IncidentSource, mon MonitorSource) *Registry {
	return NewRegistry(inc, mon, func() time.Time { return testNow })
}

func actorContext(actorID uuid.UUID) context.Context {
	return auth.ContextWithPrincipal(context.Background(), auth.Principal{UserID: actorID})
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
	workspaceID := uuid.New()

	t.Run("unknown tool", func(t *testing.T) {
		registry := newTestRegistry(nil, nil)
		if _, err := registry.Call(actorContext(uuid.New()), workspaceID, "o1_delete_everything", nil); !errors.Is(err, ErrUnknownTool) {
			t.Fatalf("err = %v, want ErrUnknownTool", err)
		}
	})

	t.Run("cross-tenant incident is not_found", func(t *testing.T) {
		incidentsSource := &fakeIncidentSource{
			get: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (incidents.IncidentDetail, error) {
				return incidents.IncidentDetail{}, incidents.ErrNotFound
			},
		}
		registry := newTestRegistry(incidentsSource, nil)
		args := json.RawMessage(`{"incident_id":"` + uuid.NewString() + `"}`)

		result, err := registry.Call(actorContext(uuid.New()), workspaceID, "o1_get_incident", args)
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
		registry := newTestRegistry(incidentsSource, nil)
		args := json.RawMessage(`{"incident_id":"` + uuid.NewString() + `"}`)

		result, err := registry.Call(actorContext(viewerID), workspaceID, "o1_get_alert_deliveries", args)
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		if !result.IsError || result.ErrorCode != "forbidden" {
			t.Fatalf("result = %+v, want forbidden", result)
		}
	})

	t.Run("bad identifier is invalid_argument", func(t *testing.T) {
		registry := newTestRegistry(&fakeIncidentSource{}, nil)
		result, err := registry.Call(actorContext(uuid.New()), workspaceID, "o1_get_incident", json.RawMessage(`{"incident_id":"not-a-uuid"}`))
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		if !result.IsError || result.ErrorCode != "invalid_argument" {
			t.Fatalf("result = %+v, want invalid_argument", result)
		}
	})
}

func TestCall_TruncatesStructurally(t *testing.T) {
	ctx := actorContext(uuid.New())
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
	registry := newTestRegistry(nil, monitorsSource)
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
	ctx := actorContext(uuid.New())
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
	registry := newTestRegistry(incidentsSource, monitorsSource)

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

func requireSuccess(t *testing.T, result Result, err error) Result {
	t.Helper()
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if result.IsError {
		t.Fatalf("result is an error: %+v", result)
	}
	return result
}

func TestCall_ArgumentMappingAndSuccess(t *testing.T) {
	ctx := actorContext(uuid.New())
	workspaceID := uuid.New()
	monitorID := uuid.New()
	incidentID := uuid.New()

	type incidentListCall struct {
		filter incidents.IncidentFilter
		limit  int
	}
	var incidentListCalls []incidentListCall
	var gotIncidentID uuid.UUID
	var gotOutcomeIncidentID uuid.UUID
	var gotMonitorID uuid.UUID
	var checksWindow time.Duration
	var checksLimit int
	var checksCursor *monitors.Cursor
	var summaryWindow time.Duration

	incident := incidents.Incident{
		ID:          incidentID,
		MonitorID:   monitorID,
		MonitorName: "api",
		Status:      "open",
		StartedAt:   testNow.Add(-time.Hour),
	}
	incidentsSource := &fakeIncidentSource{
		list: func(_ context.Context, _, _ uuid.UUID, filter incidents.IncidentFilter, _ *httpx.PageCursor, limit int) ([]incidents.Incident, error) {
			incidentListCalls = append(incidentListCalls, incidentListCall{filter: filter, limit: limit})
			if filter.Status == "open" {
				return []incidents.Incident{incident}, nil
			}
			return []incidents.Incident{}, nil
		},
		get: func(_ context.Context, _, _, id uuid.UUID) (incidents.IncidentDetail, error) {
			gotIncidentID = id
			return incidents.IncidentDetail{
				Incident: incident,
				Events: []incidents.IncidentEvent{{
					ID:        uuid.New(),
					Kind:      "opened",
					Detail:    json.RawMessage(`{"new_status":"open"}`),
					CreatedAt: testNow.Add(-time.Hour),
				}},
			}, nil
		},
		outcomes: func(_ context.Context, _, _, id uuid.UUID) ([]incidents.DeliveryOutcome, error) {
			gotOutcomeIncidentID = id
			return []incidents.DeliveryOutcome{{
				ChannelKind: "email",
				Event:       "opened",
				Status:      "sent",
				Attempts:    1,
				CreatedAt:   testNow,
			}}, nil
		},
	}
	monitorsSource := &fakeMonitorSource{
		list: func(context.Context, uuid.UUID, uuid.UUID, *monitors.Cursor, int) ([]monitors.Monitor, error) {
			return []monitors.Monitor{{ID: monitorID, Name: "api", Method: "GET", Status: "up"}}, nil
		},
		get: func(_ context.Context, _, _, id uuid.UUID) (monitors.Monitor, error) {
			gotMonitorID = id
			return monitors.Monitor{
				ID:                    id,
				Name:                  "api",
				URL:                   "https://secret.example.com/health?token=secret",
				Method:                "GET",
				Headers:               map[string]string{"Authorization": "Bearer secret"},
				ExpectedStatus:        "200",
				IntervalSeconds:       60,
				TimeoutSeconds:        10,
				ConfirmationThreshold: 3,
				RecoveryThreshold:     2,
				Status:                "up",
			}, nil
		},
		results: func(_ context.Context, _, _, _ uuid.UUID, window time.Duration, cursor *monitors.Cursor, limit int) ([]monitors.ProbeResult, error) {
			checksWindow = window
			checksCursor = cursor
			checksLimit = limit
			return []monitors.ProbeResult{{CheckedAt: testNow, Status: "down", LatencyMS: 42}}, nil
		},
		summary: func(_ context.Context, _, _, _ uuid.UUID, window time.Duration) (monitors.Summary, error) {
			summaryWindow = window
			return monitors.Summary{Checks: 1, Down: 1, P95LatencyMS: 42}, nil
		},
	}
	registry := newTestRegistry(incidentsSource, monitorsSource)

	t.Run("list incidents maps filter and limit", func(t *testing.T) {
		args := json.RawMessage(`{"monitor_id":"` + monitorID.String() + `","status":"open","limit":5}`)
		res, err := registry.Call(ctx, workspaceID, "o1_list_incidents", args)
		result := requireSuccess(t, res, err)

		data, ok := result.Data.(*incidentListData)
		if !ok {
			t.Fatalf("data = %T, want *incidentListData", result.Data)
		}
		if len(data.Incidents) != 1 || data.Incidents[0].ID != incidentID {
			t.Fatalf("incidents = %+v, want the listed incident", data.Incidents)
		}

		var call *incidentListCall
		for i := range incidentListCalls {
			if incidentListCalls[i].filter.Status == "open" {
				call = &incidentListCalls[i]
			}
		}
		if call == nil {
			t.Fatalf("incident list was not called with status open: %+v", incidentListCalls)
		}
		if call.filter.MonitorID == nil || *call.filter.MonitorID != monitorID {
			t.Fatalf("filter.MonitorID = %v, want %s", call.filter.MonitorID, monitorID)
		}
		if call.limit != 5 {
			t.Fatalf("limit = %d, want 5", call.limit)
		}
	})

	t.Run("get monitor returns the projection", func(t *testing.T) {
		args := json.RawMessage(`{"monitor_id":"` + monitorID.String() + `"}`)
		res, err := registry.Call(ctx, workspaceID, "o1_get_monitor", args)
		result := requireSuccess(t, res, err)
		if gotMonitorID != monitorID {
			t.Fatalf("monitor id = %s, want %s", gotMonitorID, monitorID)
		}

		data, ok := result.Data.(*monitorData)
		if !ok {
			t.Fatalf("data = %T, want *monitorData", result.Data)
		}
		if data.Monitor.ID != monitorID || data.Monitor.Name != "api" || data.Monitor.Method != "GET" {
			t.Fatalf("monitor = %+v, want the projected monitor", data.Monitor)
		}
		if data.Monitor.ActiveIncidentID != nil {
			t.Fatalf("active incident = %v, want nil", data.Monitor.ActiveIncidentID)
		}
		raw, err := json.Marshal(result)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		for _, secret := range []string{"secret.example.com", "Authorization", "Bearer", "token=secret"} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("leaked %q: %s", secret, raw)
			}
		}
	})

	t.Run("alert deliveries maps the incident id and returns member rows", func(t *testing.T) {
		args := json.RawMessage(`{"incident_id":"` + incidentID.String() + `"}`)
		res, err := registry.Call(ctx, workspaceID, "o1_get_alert_deliveries", args)
		result := requireSuccess(t, res, err)
		if gotOutcomeIncidentID != incidentID {
			t.Fatalf("incident id = %s, want %s", gotOutcomeIncidentID, incidentID)
		}

		data, ok := result.Data.(*deliveryListData)
		if !ok {
			t.Fatalf("data = %T, want *deliveryListData", result.Data)
		}
		if len(data.Deliveries) != 1 {
			t.Fatalf("deliveries = %d, want 1", len(data.Deliveries))
		}
		delivery := data.Deliveries[0]
		if delivery.ChannelKind != "email" || delivery.Event != "opened" || delivery.Status != "sent" || delivery.Attempts != 1 {
			t.Fatalf("delivery = %+v, want the member-safe outcome", delivery)
		}
	})

	t.Run("recent checks maps the window and limit", func(t *testing.T) {
		args := json.RawMessage(`{"monitor_id":"` + monitorID.String() + `","hours":6}`)
		res, err := registry.Call(ctx, workspaceID, "o1_get_recent_checks", args)
		result := requireSuccess(t, res, err)

		if checksWindow != 6*time.Hour || summaryWindow != 6*time.Hour {
			t.Fatalf("windows = %s/%s, want 6h", checksWindow, summaryWindow)
		}
		if checksLimit != 100 {
			t.Fatalf("result limit = %d, want 100", checksLimit)
		}
		if checksCursor != nil {
			t.Fatalf("cursor = %+v, want nil", checksCursor)
		}

		data, ok := result.Data.(*checksData)
		if !ok {
			t.Fatalf("data = %T, want *checksData", result.Data)
		}
		if len(data.Checks) != 1 || data.Checks[0].Status != "down" {
			t.Fatalf("checks = %+v, want the returned check", data.Checks)
		}
		if data.Count != 1 || data.Failures != 1 {
			t.Fatalf("aggregates = count %d failures %d, want 1/1", data.Count, data.Failures)
		}
	})

	t.Run("get incident returns the projection and timeline", func(t *testing.T) {
		args := json.RawMessage(`{"incident_id":"` + incidentID.String() + `"}`)
		res, err := registry.Call(ctx, workspaceID, "o1_get_incident", args)
		result := requireSuccess(t, res, err)
		if gotIncidentID != incidentID {
			t.Fatalf("incident id = %s, want %s", gotIncidentID, incidentID)
		}

		data, ok := result.Data.(*incidentData)
		if !ok {
			t.Fatalf("data = %T, want *incidentData", result.Data)
		}
		if data.Incident.ID != incidentID || data.Incident.MonitorID != monitorID {
			t.Fatalf("incident = %+v, want the projected incident", data.Incident)
		}
		if len(data.Events) != 1 || data.Events[0].Kind != "opened" {
			t.Fatalf("events = %+v, want the projected timeline", data.Events)
		}
	})

	t.Run("list monitors returns the projection", func(t *testing.T) {
		res, err := registry.Call(ctx, workspaceID, "o1_list_monitors", nil)
		result := requireSuccess(t, res, err)
		data, ok := result.Data.(*monitorListData)
		if !ok {
			t.Fatalf("data = %T, want *monitorListData", result.Data)
		}
		if len(data.Monitors) != 1 || data.Monitors[0].ID != monitorID || data.Monitors[0].Name != "api" {
			t.Fatalf("monitors = %+v, want the projected monitor", data.Monitors)
		}
	})
}

func TestCall_RejectsWorkspaceID(t *testing.T) {
	registry := newTestRegistry(&fakeIncidentSource{}, nil)
	args := json.RawMessage(`{"incident_id":"` + uuid.NewString() + `","workspace_id":"` + uuid.NewString() + `"}`)

	result, err := registry.Call(actorContext(uuid.New()), uuid.New(), "o1_get_incident", args)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !result.IsError || result.ErrorCode != "invalid_argument" {
		t.Fatalf("result = %+v, want invalid_argument", result)
	}
}

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
