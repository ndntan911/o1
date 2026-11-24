package copilot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"o1/internal/evidence"
	"o1/internal/incidents"
	"o1/internal/monitors"
)

// Seed is the redacted context handed to the model before it calls a tool. It
// carries no event ids, no free-text incident detail, and no monitor secret, so
// no fact in it can be cited directly: citable facts come from tool results.
type Seed struct {
	Incident   evidence.IncidentView    `json:"incident"`
	Monitor    evidence.MonitorView     `json:"monitor"`
	Timeline   []evidence.SeedEventView `json:"timeline"`
	Deliveries []evidence.DeliveryView  `json:"deliveries"`
}

// IncidentSeedSource is the incident read surface BuildSeed needs. Both methods
// authorize the actor against the workspace.
type IncidentSeedSource interface {
	Get(ctx context.Context, actorID, workspaceID, incidentID uuid.UUID) (incidents.IncidentDetail, error)
	DeliveryOutcomes(ctx context.Context, actorID, workspaceID, incidentID uuid.UUID) ([]incidents.DeliveryOutcome, error)
}

// MonitorSeedSource is the monitor read surface BuildSeed needs.
type MonitorSeedSource interface {
	Get(ctx context.Context, actorID, workspaceID, monitorID uuid.UUID) (monitors.Monitor, error)
}

// BuildSeed loads the incident, its monitor, its timeline kinds, and its alert
// delivery outcomes through the shared evidence projections. Delivery outcomes
// that the actor may not read (forbidden) or that do not exist degrade to an
// empty slice; every other lookup failure aborts the seed.
func BuildSeed(
	ctx context.Context,
	incidentSource IncidentSeedSource,
	monitorSource MonitorSeedSource,
	actorID, workspaceID, incidentID uuid.UUID,
	now time.Time,
) (Seed, error) {
	detail, err := incidentSource.Get(ctx, actorID, workspaceID, incidentID)
	if err != nil {
		return Seed{}, fmt.Errorf("copilot: load incident for seed: %w", err)
	}

	monitor, err := monitorSource.Get(ctx, actorID, workspaceID, detail.Incident.MonitorID)
	if err != nil {
		return Seed{}, fmt.Errorf("copilot: load monitor for seed: %w", err)
	}

	outcomes, err := incidentSource.DeliveryOutcomes(ctx, actorID, workspaceID, incidentID)
	if err != nil && !errors.Is(err, incidents.ErrForbidden) && !errors.Is(err, incidents.ErrNotFound) {
		return Seed{}, fmt.Errorf("copilot: load delivery outcomes for seed: %w", err)
	}

	incidentView := evidence.Incident(detail)
	// evidence.Incident derives an open incident's duration from the wall
	// clock. The seed uses the run clock instead, so a caller with a fake clock
	// gets a stable seed and a stable hash.
	if detail.Incident.ResolvedAt == nil && !now.IsZero() {
		duration := int(now.Sub(detail.Incident.StartedAt).Minutes())
		if duration < 0 {
			duration = 0
		}
		incidentView.DurationMinutes = duration
	}

	deliveries := make([]evidence.DeliveryView, 0, len(outcomes))
	for _, outcome := range outcomes {
		deliveries = append(deliveries, evidence.Delivery(outcome))
	}

	return Seed{
		Incident:   incidentView,
		Monitor:    evidence.Monitor(monitor),
		Timeline:   evidence.SeedTimeline(detail),
		Deliveries: deliveries,
	}, nil
}

// HashSeed is the hex-encoded SHA-256 of the prompt version and the seed. The
// stored hash lets a replay prove it saw the same redacted context.
func HashSeed(seed Seed, promptVersion string) string {
	payload, err := json.Marshal(struct {
		Version string `json:"version"`
		Seed    Seed   `json:"seed"`
	}{
		Version: promptVersion,
		Seed:    seed,
	})
	if err != nil {
		// Seed contains only JSON-safe values, so this is unreachable.
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
