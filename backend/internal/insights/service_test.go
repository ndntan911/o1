package insights

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"o1/internal/ai"
	"o1/internal/incidents"
	"o1/internal/monitors"
)

type fakeStore struct {
	rows    []Insight
	inserts int
	err     error
}

func (s *fakeStore) Latest(_ context.Context, workspaceID, incidentID uuid.UUID) (*Insight, error) {
	if s.err != nil {
		return nil, s.err
	}
	for index := len(s.rows) - 1; index >= 0; index-- {
		row := s.rows[index]
		if row.WorkspaceID == workspaceID && row.IncidentID == incidentID {
			copy := row
			return &copy, nil
		}
	}
	return nil, nil
}

func (s *fakeStore) Insert(_ context.Context, insight Insight) (Insight, error) {
	if s.err != nil {
		return Insight{}, s.err
	}
	s.inserts++
	insight.ID = uuid.New()
	s.rows = append(s.rows, insight)
	return insight, nil
}

type fakeIncidents struct {
	detail       incidents.IncidentDetail
	deliveries   []incidents.Delivery
	err          error
	deliveryErr  error
}

func (f *fakeIncidents) Get(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (incidents.IncidentDetail, error) {
	return f.detail, f.err
}

func (f *fakeIncidents) Deliveries(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) ([]incidents.Delivery, error) {
	if f.deliveryErr != nil {
		return nil, f.deliveryErr
	}
	return f.deliveries, f.err
}

type fakeMonitors struct {
	monitor monitors.Monitor
	err     error
}

func (f *fakeMonitors) Get(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (monitors.Monitor, error) {
	return f.monitor, f.err
}

func serviceFixture(t *testing.T, reply string) (*Service, *fakeStore, *fakeIncidents, uuid.UUID, uuid.UUID) {
	t.Helper()

	detail, deliveries, monitor := fixture()
	store := &fakeStore{}
	incidentsSource := &fakeIncidents{detail: detail, deliveries: deliveries}
	monitorsSource := &fakeMonitors{monitor: *monitor}

	svc := NewService(Options{
		Store:     store,
		Provider:  &ai.Stub{Reply: reply},
		Model:     "stub-model",
		Incidents: incidentsSource,
		Monitors:  monitorsSource,
		Now:       func() time.Time { return time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC) },
	})
	return svc, store, incidentsSource, detail.Incident.WorkspaceID, detail.Incident.ID
}

func TestGenerate_StoresAValidatedAnswer(t *testing.T) {
	detail := incidents.IncidentDetail{}
	_ = detail
	svc, store, _, workspaceID, incidentID := serviceFixture(t, "")

	eventID := svcState(t, svc).Events[0].ID
	reply := `{"sufficient":true,"assessment":"Target timeouts.","hypotheses":[
		{"rank":1,"cause":"Timeouts above the 10s limit","confidence":"high",
		 "evidence":[{"kind":"event","event_id":"` + eventID.String() + `","note":"consecutive failures"}],
		 "falsified_by":"a success in the same window"}]}`
	svc.provider = &ai.Stub{Reply: reply}

	actor := uuid.New()
	insight, err := svc.Generate(context.Background(), actor, workspaceID, incidentID, false)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if insight.Status != StatusOK {
		t.Fatalf("status = %q", insight.Status)
	}
	if len(insight.Result.Hypotheses) != 1 {
		t.Fatalf("hypotheses = %+v", insight.Result.Hypotheses)
	}
	if insight.Result.Hypotheses[0].Unsupported {
		t.Fatal("a citation to a real event must not be marked unsupported")
	}
	if store.inserts != 1 {
		t.Fatalf("inserts = %d", store.inserts)
	}
	if insight.CreatedBy == nil || *insight.CreatedBy != actor {
		t.Fatal("the actor must be recorded")
	}
}

func TestGenerate_RefusesToRepeatWithoutForce(t *testing.T) {
	svc, store, _, workspaceID, incidentID := serviceFixture(t, `{"sufficient":false,"assessment":"thin data","hypotheses":[]}`)
	actor := uuid.New()

	if _, err := svc.Generate(context.Background(), actor, workspaceID, incidentID, false); err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	_, err := svc.Generate(context.Background(), actor, workspaceID, incidentID, false)
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second Generate: %v", err)
	}
	if store.inserts != 1 {
		t.Fatalf("inserts = %d", store.inserts)
	}

	if _, err := svc.Generate(context.Background(), actor, workspaceID, incidentID, true); err != nil {
		t.Fatalf("forced Generate: %v", err)
	}
	if store.inserts != 2 {
		t.Fatalf("inserts after force = %d", store.inserts)
	}
}

func TestGenerate_StoresFailureWhenTheModelErrors(t *testing.T) {
	svc, store, _, workspaceID, incidentID := serviceFixture(t, "")
	svc.provider = &ai.Stub{Err: errors.New("upstream unavailable")}

	insight, err := svc.Generate(context.Background(), uuid.New(), workspaceID, incidentID, false)
	if err == nil {
		t.Fatal("expected the model error to surface")
	}
	if insight.Status != StatusFailed {
		t.Fatalf("status = %q", insight.Status)
	}
	if insight.Result.Hypotheses == nil {
		t.Fatal("failures must still marshal hypotheses as an array")
	}
	if store.inserts != 1 {
		t.Fatalf("a failed generation is still recorded, inserts = %d", store.inserts)
	}
}

func TestGenerate_UnreadableAnswerBecomesFailed(t *testing.T) {
	svc, _, _, workspaceID, incidentID := serviceFixture(t, "the service just looks slow to me")

	insight, err := svc.Generate(context.Background(), uuid.New(), workspaceID, incidentID, false)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if insight.Status != StatusFailed {
		t.Fatalf("status = %q", insight.Status)
	}
	if len(insight.Result.Hypotheses) != 0 {
		t.Fatal("nothing may be shown from an unreadable answer")
	}
}

func TestGenerate_ThinDataIsInsufficient(t *testing.T) {
	svc, _, _, workspaceID, incidentID := serviceFixture(t, `{"sufficient":false,"assessment":"one check only","hypotheses":[]}`)

	insight, err := svc.Generate(context.Background(), uuid.New(), workspaceID, incidentID, false)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if insight.Status != StatusInsufficient {
		t.Fatalf("status = %q", insight.Status)
	}
}

func TestGenerate_DisabledWithoutProvider(t *testing.T) {
	svc := NewService(Options{Store: &fakeStore{}})
	if svc.Enabled() {
		t.Fatal("a service without a provider must be disabled")
	}
	if _, err := svc.Generate(context.Background(), uuid.New(), uuid.New(), uuid.New(), false); !errors.Is(err, ErrDisabled) {
		t.Fatalf("err = %v", err)
	}
}

// svcState exposes the fixture's incident detail through the fake source so a
// test can cite a real event id.
func svcState(t *testing.T, svc *Service) incidents.IncidentDetail {
	t.Helper()
	return svc.incidents.(*fakeIncidents).detail
}

func TestGenerate_SurvivesUnreadableDeliveries(t *testing.T) {
	// A viewer cannot read delivery history; the analysis must still happen.
	svc, _, source, workspaceID, incidentID := serviceFixture(t, `{"sufficient":false,"assessment":"thin","hypotheses":[]}`)
	source.deliveryErr = errors.New("forbidden")

	insight, err := svc.Generate(context.Background(), uuid.New(), workspaceID, incidentID, false)
	if err != nil {
		t.Fatalf("Generate must not fail when deliveries are unavailable: %v", err)
	}
	if insight.Status != StatusInsufficient {
		t.Fatalf("status = %q", insight.Status)
	}
}
