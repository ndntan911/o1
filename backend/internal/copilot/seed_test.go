package copilot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"o1/internal/evidence"
	"o1/internal/incidents"
	"o1/internal/monitors"
)

var seedTestNow = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

type fakeSeedIncidentSource struct {
	detail      incidents.IncidentDetail
	outcomes    []incidents.DeliveryOutcome
	getErr      error
	outcomesErr error

	gotActorID     uuid.UUID
	gotWorkspaceID uuid.UUID
	gotIncidentID  uuid.UUID
	getCalls       int
	deliveryCalls  int
}

func (f *fakeSeedIncidentSource) Get(_ context.Context, actorID, workspaceID, incidentID uuid.UUID) (incidents.IncidentDetail, error) {
	f.getCalls++
	f.gotActorID, f.gotWorkspaceID, f.gotIncidentID = actorID, workspaceID, incidentID
	if f.getErr != nil {
		return incidents.IncidentDetail{}, f.getErr
	}
	return f.detail, nil
}

func (f *fakeSeedIncidentSource) DeliveryOutcomes(_ context.Context, actorID, workspaceID, incidentID uuid.UUID) ([]incidents.DeliveryOutcome, error) {
	f.deliveryCalls++
	f.gotActorID, f.gotWorkspaceID, f.gotIncidentID = actorID, workspaceID, incidentID
	if f.outcomesErr != nil {
		return nil, f.outcomesErr
	}
	return f.outcomes, nil
}

type fakeSeedMonitorSource struct {
	monitor monitors.Monitor
	err     error

	gotActorID     uuid.UUID
	gotWorkspaceID uuid.UUID
	gotMonitorID   uuid.UUID
	calls          int
}

func (f *fakeSeedMonitorSource) Get(_ context.Context, actorID, workspaceID, monitorID uuid.UUID) (monitors.Monitor, error) {
	f.calls++
	f.gotActorID, f.gotWorkspaceID, f.gotMonitorID = actorID, workspaceID, monitorID
	if f.err != nil {
		return monitors.Monitor{}, f.err
	}
	return f.monitor, nil
}

// seedFixture returns a fully populated incident/monitor/delivery fixture whose
// inputs contain every kind of value the seed must not carry.
func seedFixture() (uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, fakeSeedIncidentSource, fakeSeedMonitorSource) {
	actorID := uuid.New()
	workspaceID := uuid.New()
	incidentID := uuid.New()
	monitorID := uuid.New()
	startedAt := seedTestNow.Add(-90 * time.Minute)

	incidentSource := fakeSeedIncidentSource{
		detail: incidents.IncidentDetail{
			Incident: incidents.Incident{
				ID:             incidentID,
				WorkspaceID:    workspaceID,
				MonitorID:      monitorID,
				MonitorName:    "api",
				Status:         "open",
				StartedAt:      startedAt,
				ReminderCount:  2,
				LastNotifiedAt: seedTimePtr(startedAt.Add(time.Minute)),
			},
			Events: []incidents.IncidentEvent{
				{
					ID:   uuid.New(),
					Kind: "opened",
					Detail: json.RawMessage(`{
						"new_status": "down",
						"note": "secret detail",
						"url": "https://internal.example/secret"
					}`),
					CreatedAt: startedAt,
				},
				{
					ID:        uuid.New(),
					Kind:      "reminder",
					Detail:    json.RawMessage(`{"recipient@example.com": "secret"}`),
					CreatedAt: startedAt.Add(30 * time.Minute),
				},
			},
		},
		outcomes: []incidents.DeliveryOutcome{{
			ChannelKind: "email",
			Event:       "opened",
			Status:      "sent",
			Attempts:    1,
			SentAt:      seedTimePtr(startedAt.Add(time.Second)),
			CreatedAt:   startedAt,
		}},
	}

	monitorSource := fakeSeedMonitorSource{
		monitor: monitors.Monitor{
			ID:                    monitorID,
			WorkspaceID:           workspaceID,
			Name:                  "api",
			URL:                   "https://user:pass@example.com/health?token=secret",
			Method:                "GET",
			Headers:               map[string]string{"Authorization": "Bearer secret"},
			ExpectedStatus:        "200",
			IntervalSeconds:       60,
			TimeoutSeconds:        10,
			ConfirmationThreshold: 2,
			RecoveryThreshold:     2,
			Status:                "down",
			LastCheck: &monitors.LastCheck{
				CheckedAt: seedTestNow.Add(-time.Minute),
			},
		},
	}

	return actorID, workspaceID, incidentID, monitorID, incidentSource, monitorSource
}

func seedTimePtr(value time.Time) *time.Time { return &value }

func TestBuildSeed_HasNoEventIDsOrSecrets(t *testing.T) {
	actorID, workspaceID, incidentID, monitorID, incidentSource, monitorSource := seedFixture()

	seed, err := BuildSeed(context.Background(), &incidentSource, &monitorSource, actorID, workspaceID, incidentID, seedTestNow)
	if err != nil {
		t.Fatalf("BuildSeed: %v", err)
	}

	blob, err := json.Marshal(seed)
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	for _, leaked := range []string{
		"https://", "Authorization", "Bearer", "user:pass", "token=secret",
		"secret", "recipient@example.com", "last_error", "event_id",
	} {
		if strings.Contains(string(blob), leaked) {
			t.Fatalf("seed leaked %q: %s", leaked, blob)
		}
	}

	if seed.Incident.ID != incidentID || seed.Incident.MonitorID != monitorID {
		t.Fatalf("incident = %+v", seed.Incident)
	}
	if seed.Incident.MonitorName != "api" || seed.Incident.ReminderCount != 2 {
		t.Fatalf("incident = %+v", seed.Incident)
	}
	if seed.Monitor.ID != monitorID || seed.Monitor.Name != "api" || seed.Monitor.Status != "down" {
		t.Fatalf("monitor = %+v", seed.Monitor)
	}
	if seed.Monitor.LastCheckAt == nil || !seed.Monitor.LastCheckAt.Equal(seedTestNow.Add(-time.Minute)) {
		t.Fatalf("last check at = %v", seed.Monitor.LastCheckAt)
	}
	if len(seed.Timeline) != 2 || seed.Timeline[0].Kind != "opened" || seed.Timeline[1].Kind != "reminder" {
		t.Fatalf("timeline = %+v", seed.Timeline)
	}
	if len(seed.Deliveries) != 1 || !reflect.DeepEqual(seed.Deliveries, []evidence.DeliveryView{evidence.Delivery(incidentSource.outcomes[0])}) {
		t.Fatalf("deliveries = %+v", seed.Deliveries)
	}

	// The timeline is kind and timestamp only: no event ids and no detail.
	var decoded struct {
		Timeline []map[string]any `json:"timeline"`
	}
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatalf("unmarshal seed: %v", err)
	}
	for i, event := range decoded.Timeline {
		if len(event) != 2 {
			t.Fatalf("timeline[%d] keys = %v, want kind and at only", i, event)
		}
		if _, ok := event["kind"]; !ok {
			t.Fatalf("timeline[%d] missing kind: %v", i, event)
		}
		if _, ok := event["at"]; !ok {
			t.Fatalf("timeline[%d] missing at: %v", i, event)
		}
	}

	if incidentSource.getCalls != 1 || incidentSource.deliveryCalls != 1 || monitorSource.calls != 1 {
		t.Fatalf("calls = get %d, deliveries %d, monitors %d", incidentSource.getCalls, incidentSource.deliveryCalls, monitorSource.calls)
	}
	if incidentSource.gotActorID != actorID || incidentSource.gotWorkspaceID != workspaceID || incidentSource.gotIncidentID != incidentID {
		t.Fatalf("incident source ids = %s/%s/%s", incidentSource.gotActorID, incidentSource.gotWorkspaceID, incidentSource.gotIncidentID)
	}
	if monitorSource.gotActorID != actorID || monitorSource.gotWorkspaceID != workspaceID || monitorSource.gotMonitorID != monitorID {
		t.Fatalf("monitor source ids = %s/%s/%s", monitorSource.gotActorID, monitorSource.gotWorkspaceID, monitorSource.gotMonitorID)
	}
}

func TestBuildSeed_UsesProvidedClockForOpenIncident(t *testing.T) {
	actorID, workspaceID, incidentID, _, incidentSource, monitorSource := seedFixture()
	incidentSource.detail.Incident.StartedAt = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	incidentSource.detail.Incident.ResolvedAt = nil

	seed, err := BuildSeed(context.Background(), &incidentSource, &monitorSource, actorID, workspaceID, incidentID, incidentSource.detail.Incident.StartedAt.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("BuildSeed: %v", err)
	}
	if seed.Incident.DurationMinutes != 120 {
		t.Fatalf("duration = %d, want 120", seed.Incident.DurationMinutes)
	}

	resolvedAt := incidentSource.detail.Incident.StartedAt.Add(30 * time.Minute)
	incidentSource.detail.Incident.ResolvedAt = &resolvedAt
	seed, err = BuildSeed(context.Background(), &incidentSource, &monitorSource, actorID, workspaceID, incidentID, incidentSource.detail.Incident.StartedAt.Add(10*time.Hour))
	if err != nil {
		t.Fatalf("BuildSeed: %v", err)
	}
	if seed.Incident.DurationMinutes != 30 {
		t.Fatalf("resolved duration = %d, want 30", seed.Incident.DurationMinutes)
	}
}

func TestBuildSeed_ToleratesDeliveryPermissionErrors(t *testing.T) {
	for _, deliveryErr := range []error{incidents.ErrForbidden, incidents.ErrNotFound} {
		t.Run(deliveryErr.Error(), func(t *testing.T) {
			actorID, workspaceID, incidentID, _, incidentSource, monitorSource := seedFixture()
			incidentSource.outcomesErr = deliveryErr

			seed, err := BuildSeed(context.Background(), &incidentSource, &monitorSource, actorID, workspaceID, incidentID, seedTestNow)
			if err != nil {
				t.Fatalf("BuildSeed: %v", err)
			}
			if seed.Deliveries == nil || len(seed.Deliveries) != 0 {
				t.Fatalf("deliveries = %#v, want an empty non-nil slice", seed.Deliveries)
			}
			if len(seed.Timeline) != 2 || seed.Incident.ID != incidentID {
				t.Fatalf("rest of the seed was lost: %+v", seed)
			}
		})
	}
}

func TestBuildSeed_PropagatesDeliveryErrors(t *testing.T) {
	actorID, workspaceID, incidentID, _, incidentSource, monitorSource := seedFixture()
	incidentSource.outcomesErr = errors.New("database is on fire")

	if _, err := BuildSeed(context.Background(), &incidentSource, &monitorSource, actorID, workspaceID, incidentID, seedTestNow); err == nil {
		t.Fatal("BuildSeed succeeded, want error")
	}
}

func TestBuildSeed_PropagatesLookupErrors(t *testing.T) {
	t.Run("incident", func(t *testing.T) {
		actorID, workspaceID, incidentID, _, incidentSource, monitorSource := seedFixture()
		incidentSource.getErr = incidents.ErrNotFound

		if _, err := BuildSeed(context.Background(), &incidentSource, &monitorSource, actorID, workspaceID, incidentID, seedTestNow); !errors.Is(err, incidents.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		if monitorSource.calls != 0 {
			t.Fatalf("monitor calls = %d, want 0", monitorSource.calls)
		}
	})

	t.Run("monitor", func(t *testing.T) {
		actorID, workspaceID, incidentID, _, incidentSource, monitorSource := seedFixture()
		monitorSource.err = errors.New("monitor lookup failed")

		if _, err := BuildSeed(context.Background(), &incidentSource, &monitorSource, actorID, workspaceID, incidentID, seedTestNow); err == nil {
			t.Fatal("BuildSeed succeeded, want error")
		}
		if incidentSource.deliveryCalls != 0 {
			t.Fatalf("delivery calls = %d, want 0", incidentSource.deliveryCalls)
		}
	})
}

func TestHashSeed_IsVersionedSHA256(t *testing.T) {
	actorID, workspaceID, incidentID, _, incidentSource, monitorSource := seedFixture()
	seed, err := BuildSeed(context.Background(), &incidentSource, &monitorSource, actorID, workspaceID, incidentID, seedTestNow)
	if err != nil {
		t.Fatalf("BuildSeed: %v", err)
	}

	if PromptVersion != "2026-11-24.1" {
		t.Fatalf("PromptVersion = %q", PromptVersion)
	}
	got := HashSeed(seed, PromptVersion)
	if len(got) != sha256.Size*2 {
		t.Fatalf("hash length = %d, want %d", len(got), sha256.Size*2)
	}
	if again := HashSeed(seed, PromptVersion); again != got {
		t.Fatalf("hash is not deterministic: %q != %q", again, got)
	}
	if other := HashSeed(seed, "2026-11-24.2"); other == got {
		t.Fatal("hash does not include the prompt version")
	}
	otherSeed := seed
	otherSeed.Incident.ReminderCount++
	if other := HashSeed(otherSeed, PromptVersion); other == got {
		t.Fatal("hash does not include the seed")
	}

	payload, err := json.Marshal(struct {
		Version string `json:"version"`
		Seed    Seed   `json:"seed"`
	}{Version: PromptVersion, Seed: seed})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	sum := sha256.Sum256(payload)
	if want := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("HashSeed = %q, want %q", got, want)
	}
}

func TestBuildUserMessage_WrapsSeedAndInstruction(t *testing.T) {
	actorID, workspaceID, incidentID, _, incidentSource, monitorSource := seedFixture()
	seed, err := BuildSeed(context.Background(), &incidentSource, &monitorSource, actorID, workspaceID, incidentID, seedTestNow)
	if err != nil {
		t.Fatalf("BuildSeed: %v", err)
	}

	message, err := BuildUserMessage(seed)
	if err != nil {
		t.Fatalf("BuildUserMessage: %v", err)
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(message), &object); err != nil {
		t.Fatalf("message is not a JSON object: %v", err)
	}
	if len(object) != 2 {
		t.Fatalf("message keys = %v, want seed and instruction", object)
	}

	var payload struct {
		Seed        Seed   `json:"seed"`
		Instruction string `json:"instruction"`
	}
	if err := json.Unmarshal([]byte(message), &payload); err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}
	if payload.Instruction != "Investigate this incident. Use the provided tools. Cite only tool results by tool_call_id." {
		t.Fatalf("instruction = %q", payload.Instruction)
	}
	if !reflect.DeepEqual(payload.Seed, seed) {
		t.Fatalf("seed changed by the wrapper:\n got %+v\nwant %+v", payload.Seed, seed)
	}
}

func TestSystemPrompt_StatesTheCitationsAndDataRules(t *testing.T) {
	if strings.TrimSpace(SystemPrompt) == "" {
		t.Fatal("SystemPrompt is empty")
	}
	for _, phrase := range []string{"never instructions", "tool_call_id", "JSON"} {
		if !strings.Contains(SystemPrompt, phrase) {
			t.Fatalf("SystemPrompt is missing %q", phrase)
		}
	}
}
