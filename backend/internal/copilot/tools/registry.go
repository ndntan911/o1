package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"o1/internal/auth"
	"o1/internal/evidence"
	"o1/internal/httpx"
	"o1/internal/incidents"
	"o1/internal/monitors"
	"o1/internal/workspaces"
)

// maxResultBytes is the tool-result cap. Arrays are truncated structurally so
// the {data, meta} wrapper survives and JSON Pointers into returned items keep
// resolving.
const maxResultBytes = 8192

// activeIncidentLimit bounds the active-incident lookup that fills
// MonitorView.ActiveIncidentID. The incident service caps a page at 100.
const activeIncidentLimit = 100

// ErrUnknownTool is returned for a tool name outside the fixed registry. Only
// programming errors reach this path; it is not a model-facing result.
var ErrUnknownTool = errors.New("copilot tools: unknown tool")

// IncidentSource is the incident service surface the registry needs. The
// methods authorize a viewer, except DeliveryOutcomes which requires a member.
type IncidentSource interface {
	Get(ctx context.Context, actorID, workspaceID, incidentID uuid.UUID) (incidents.IncidentDetail, error)
	List(ctx context.Context, actorID, workspaceID uuid.UUID, filter incidents.IncidentFilter, cursor *httpx.PageCursor, limit int) ([]incidents.Incident, error)
	DeliveryOutcomes(ctx context.Context, actorID, workspaceID, incidentID uuid.UUID) ([]incidents.DeliveryOutcome, error)
}

// MonitorSource is the monitor service surface the registry needs. Every
// method authorizes a viewer.
type MonitorSource interface {
	Get(ctx context.Context, actorID, workspaceID, monitorID uuid.UUID) (monitors.Monitor, error)
	List(ctx context.Context, actorID, workspaceID uuid.UUID, cursor *monitors.Cursor, limit int) ([]monitors.Monitor, error)
	Results(ctx context.Context, actorID, workspaceID, monitorID uuid.UUID, window time.Duration, cursor *monitors.Cursor, limit int) ([]monitors.ProbeResult, error)
	Summary(ctx context.Context, actorID, workspaceID, monitorID uuid.UUID, window time.Duration) (monitors.Summary, error)
}

// Registry dispatches the six read-only tools. It never accepts a workspace id
// from the model: the caller supplies the workspace and the actor comes from
// the authenticated principal in the context.
type Registry struct {
	incidents IncidentSource
	monitors  MonitorSource
	now       func() time.Time
}

// NewRegistry wires a registry around the domain services and a clock.
func NewRegistry(incidents IncidentSource, monitors MonitorSource, now func() time.Time) *Registry {
	if now == nil {
		now = time.Now
	}
	return &Registry{incidents: incidents, monitors: monitors, now: now}
}

// Specs returns the six tool definitions in spec order.
func (r *Registry) Specs() []Spec { return Specs() }

// Call runs one tool. Unknown names return ErrUnknownTool; every other failure
// is a model-facing Result with IsError set and a bounded ErrorCode.
func (r *Registry) Call(ctx context.Context, workspaceID uuid.UUID, name string, args json.RawMessage) (Result, error) {
	if _, ok := specFor(name); !ok {
		return Result{}, fmt.Errorf("%w: %s", ErrUnknownTool, name)
	}
	principal, ok := auth.PrincipalFrom(ctx)
	if !ok {
		return r.errorResult("forbidden"), nil
	}
	actorID := principal.UserID

	switch name {
	case "o1_list_monitors":
		return r.listMonitors(ctx, actorID, workspaceID, args), nil
	case "o1_get_monitor":
		return r.getMonitor(ctx, actorID, workspaceID, args), nil
	case "o1_list_incidents":
		return r.listIncidents(ctx, actorID, workspaceID, args), nil
	case "o1_get_incident":
		return r.getIncident(ctx, actorID, workspaceID, args), nil
	case "o1_get_recent_checks":
		return r.recentChecks(ctx, actorID, workspaceID, args), nil
	case "o1_get_alert_deliveries":
		return r.alertDeliveries(ctx, actorID, workspaceID, args), nil
	default:
		return Result{}, fmt.Errorf("%w: %s", ErrUnknownTool, name)
	}
}

type listMonitorsArgs struct {
	Limit *int `json:"limit"`
}

type getMonitorArgs struct {
	MonitorID string `json:"monitor_id"`
}

type listIncidentsArgs struct {
	MonitorID *string `json:"monitor_id"`
	Status    *string `json:"status"`
	Limit     *int    `json:"limit"`
}

type incidentIDArgs struct {
	IncidentID string `json:"incident_id"`
}

type recentChecksArgs struct {
	MonitorID string `json:"monitor_id"`
	Hours     *int   `json:"hours"`
}

// monitorListData is the o1_list_monitors payload.
type monitorListData struct {
	Monitors []evidence.MonitorView `json:"monitors"`
}

func (d *monitorListData) length() int { return len(d.Monitors) }
func (d *monitorListData) keep(n int)  { d.Monitors = d.Monitors[:n] }

// monitorData is the o1_get_monitor payload.
type monitorData struct {
	Monitor evidence.MonitorView `json:"monitor"`
}

// incidentListData is the o1_list_incidents payload.
type incidentListData struct {
	Incidents []evidence.IncidentView `json:"incidents"`
}

func (d *incidentListData) length() int { return len(d.Incidents) }
func (d *incidentListData) keep(n int)  { d.Incidents = d.Incidents[:n] }

// incidentData is the o1_get_incident payload.
type incidentData struct {
	Incident evidence.IncidentView `json:"incident"`
	Events   []evidence.EventView  `json:"events"`
}

func (d *incidentData) length() int { return len(d.Events) }
func (d *incidentData) keep(n int)  { d.Events = d.Events[:n] }

// checksData flattens evidence.ChecksView into the {data, meta} envelope and
// exposes its checks array to structural truncation.
type checksData struct {
	evidence.ChecksView
}

func (d *checksData) length() int { return len(d.Checks) }
func (d *checksData) keep(n int)  { d.Checks = d.Checks[:n] }

// deliveryListData is the o1_get_alert_deliveries payload.
type deliveryListData struct {
	Deliveries []evidence.DeliveryView `json:"deliveries"`
}

func (d *deliveryListData) length() int { return len(d.Deliveries) }
func (d *deliveryListData) keep(n int)  { d.Deliveries = d.Deliveries[:n] }

// truncator is implemented by each payload that has a primary array. Only that
// array is shortened; the wrapper and the aggregate fields stay intact.
type truncator interface {
	length() int
	keep(n int)
}

func (r *Registry) listMonitors(ctx context.Context, actorID, workspaceID uuid.UUID, raw json.RawMessage) Result {
	var args listMonitorsArgs
	if err := decodeArgs(raw, &args); err != nil {
		return r.errorResult("invalid_argument")
	}
	limit, ok := limitArg(args.Limit, 50, 1, 50)
	if !ok {
		return r.errorResult("invalid_argument")
	}

	listed, err := r.monitors.List(ctx, actorID, workspaceID, nil, limit)
	if err != nil {
		return r.resultFromError(err)
	}
	// One active-incident listing fills every returned monitor, avoiding a
	// per-monitor lookup. The incident service enforces viewer authorization.
	active, err := r.incidents.List(ctx, actorID, workspaceID, incidents.IncidentFilter{Status: "active"}, nil, activeIncidentLimit)
	if err != nil {
		return r.resultFromError(err)
	}
	activeByMonitor := make(map[uuid.UUID]uuid.UUID, len(active))
	for _, incident := range active {
		activeByMonitor[incident.MonitorID] = incident.ID
	}

	views := make([]evidence.MonitorView, 0, len(listed))
	for _, monitor := range listed {
		view := evidence.Monitor(monitor)
		if incidentID, ok := activeByMonitor[view.ID]; ok {
			activeIncidentID := incidentID
			view.ActiveIncidentID = &activeIncidentID
		}
		views = append(views, view)
	}

	data := &monitorListData{Monitors: views}
	return r.finish(data, len(views), data)
}

func (r *Registry) getMonitor(ctx context.Context, actorID, workspaceID uuid.UUID, raw json.RawMessage) Result {
	var args getMonitorArgs
	if err := decodeArgs(raw, &args); err != nil {
		return r.errorResult("invalid_argument")
	}
	monitorID, ok := parseID(args.MonitorID)
	if !ok {
		return r.errorResult("invalid_argument")
	}

	monitor, err := r.monitors.Get(ctx, actorID, workspaceID, monitorID)
	if err != nil {
		return r.resultFromError(err)
	}
	view := evidence.Monitor(monitor)

	active, err := r.incidents.List(ctx, actorID, workspaceID, incidents.IncidentFilter{
		Status:    "active",
		MonitorID: &monitorID,
	}, nil, 1)
	if err != nil {
		return r.resultFromError(err)
	}
	if len(active) > 0 {
		activeIncidentID := active[0].ID
		view.ActiveIncidentID = &activeIncidentID
	}

	return r.finish(&monitorData{Monitor: view}, 1, nil)
}

func (r *Registry) listIncidents(ctx context.Context, actorID, workspaceID uuid.UUID, raw json.RawMessage) Result {
	var args listIncidentsArgs
	if err := decodeArgs(raw, &args); err != nil {
		return r.errorResult("invalid_argument")
	}
	limit, ok := limitArg(args.Limit, 20, 1, 20)
	if !ok {
		return r.errorResult("invalid_argument")
	}

	filter := incidents.IncidentFilter{}
	if args.Status != nil {
		if !validStatus(*args.Status) {
			return r.errorResult("invalid_argument")
		}
		filter.Status = *args.Status
	}
	if args.MonitorID != nil {
		monitorID, ok := parseID(*args.MonitorID)
		if !ok {
			return r.errorResult("invalid_argument")
		}
		filter.MonitorID = &monitorID
	}

	listed, err := r.incidents.List(ctx, actorID, workspaceID, filter, nil, limit)
	if err != nil {
		return r.resultFromError(err)
	}
	views := make([]evidence.IncidentView, 0, len(listed))
	for _, incident := range listed {
		views = append(views, evidence.Incident(incidents.IncidentDetail{Incident: incident}))
	}

	data := &incidentListData{Incidents: views}
	return r.finish(data, len(views), data)
}

func (r *Registry) getIncident(ctx context.Context, actorID, workspaceID uuid.UUID, raw json.RawMessage) Result {
	var args incidentIDArgs
	if err := decodeArgs(raw, &args); err != nil {
		return r.errorResult("invalid_argument")
	}
	incidentID, ok := parseID(args.IncidentID)
	if !ok {
		return r.errorResult("invalid_argument")
	}

	detail, err := r.incidents.Get(ctx, actorID, workspaceID, incidentID)
	if err != nil {
		return r.resultFromError(err)
	}
	data := &incidentData{
		Incident: evidence.Incident(detail),
		Events:   evidence.Timeline(detail),
	}
	return r.finish(data, len(data.Events), data)
}

func (r *Registry) recentChecks(ctx context.Context, actorID, workspaceID uuid.UUID, raw json.RawMessage) Result {
	var args recentChecksArgs
	if err := decodeArgs(raw, &args); err != nil {
		return r.errorResult("invalid_argument")
	}
	monitorID, ok := parseID(args.MonitorID)
	if !ok {
		return r.errorResult("invalid_argument")
	}
	hours, ok := limitArg(args.Hours, 24, 1, 168)
	if !ok {
		return r.errorResult("invalid_argument")
	}
	window := time.Duration(hours) * time.Hour

	results, err := r.monitors.Results(ctx, actorID, workspaceID, monitorID, window, nil, 100)
	if err != nil {
		return r.resultFromError(err)
	}
	summary, err := r.monitors.Summary(ctx, actorID, workspaceID, monitorID, window)
	if err != nil {
		return r.resultFromError(err)
	}

	data := &checksData{ChecksView: evidence.Checks(results, summary, hours)}
	// Count comes from the full-window summary; Total is the number of fetched
	// checks before truncation.
	return r.finish(data, int(summary.Checks), data)
}

func (r *Registry) alertDeliveries(ctx context.Context, actorID, workspaceID uuid.UUID, raw json.RawMessage) Result {
	var args incidentIDArgs
	if err := decodeArgs(raw, &args); err != nil {
		return r.errorResult("invalid_argument")
	}
	incidentID, ok := parseID(args.IncidentID)
	if !ok {
		return r.errorResult("invalid_argument")
	}

	outcomes, err := r.incidents.DeliveryOutcomes(ctx, actorID, workspaceID, incidentID)
	if err != nil {
		return r.resultFromError(err)
	}
	views := make([]evidence.DeliveryView, 0, len(outcomes))
	for _, outcome := range outcomes {
		views = append(views, evidence.Delivery(outcome))
	}

	data := &deliveryListData{Deliveries: views}
	return r.finish(data, len(views), data)
}

// finish builds the {data, meta} envelope and structurally truncates the
// payload's primary array until it fits. Count and Total stay at their
// full-window values; only Returned changes.
func (r *Registry) finish(data any, count int, target truncator) Result {
	total := 1
	if target != nil {
		total = target.length()
	}
	meta := Meta{
		AsOf:     r.now().UTC(),
		Count:    count,
		Total:    total,
		Returned: total,
	}

	for {
		raw, err := json.Marshal(struct {
			Data any  `json:"data"`
			Meta Meta `json:"meta"`
		}{Data: data, Meta: meta})
		if err != nil {
			return r.errorResult("internal")
		}
		if len(raw) <= maxResultBytes {
			break
		}
		if target == nil || target.length() == 0 {
			// There is no array left to drop. The wrapper must still survive,
			// so return the bounded payload rather than cutting bytes.
			break
		}
		meta.Truncated = true
		next := target.length() / 2
		target.keep(next)
		meta.Returned = next
	}

	return Result{Data: data, Meta: meta}
}

// errorResult builds a bounded model-facing failure.
func (r *Registry) errorResult(code string) Result {
	return Result{
		IsError:   true,
		ErrorCode: code,
		Meta:      Meta{AsOf: r.now().UTC()},
	}
}

// resultFromError maps service sentinels onto the bounded tool error codes.
// Anything else becomes internal; the raw error is never copied into a result.
func (r *Registry) resultFromError(err error) Result {
	switch {
	case errors.Is(err, workspaces.ErrForbidden):
		return r.errorResult("forbidden")
	case errors.Is(err, workspaces.ErrNotFound):
		return r.errorResult("not_found")
	case errors.Is(err, workspaces.ErrValidation):
		return r.errorResult("invalid_argument")
	}
	var rateLimited *monitors.RateLimitError
	if errors.As(err, &rateLimited) {
		return r.errorResult("rate_limited")
	}
	return r.errorResult("internal")
}

// decodeArgs strictly decodes tool arguments. An empty payload is an empty
// object so tools with only optional fields can be called with no arguments.
func decodeArgs(raw json.RawMessage, dst any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing data")
	}
	return nil
}

// limitArg applies a default and rejects out-of-range explicit values.
func limitArg(value *int, defaultValue, minimum, maximum int) (int, bool) {
	if value == nil {
		return defaultValue, true
	}
	if *value < minimum || *value > maximum {
		return 0, false
	}
	return *value, true
}

// parseID parses a required UUID argument.
func parseID(value string) (uuid.UUID, bool) {
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, false
	}
	return id, true
}

// validStatus mirrors incidents.IncidentFilter's accepted status values.
func validStatus(status string) bool {
	switch status {
	case "active", "open", "acknowledged", "resolved", "all":
		return true
	default:
		return false
	}
}
