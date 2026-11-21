// Package insights turns an incident's own data into a short, cited analysis:
// an assessment and up to three ranked hypotheses, each tied to evidence that
// exists in the incident timeline. The model writes the prose; this package
// decides what it is allowed to see and what it is allowed to claim.
package insights

import (
	"time"

	"github.com/google/uuid"
)

// PromptVersion identifies the prompt and output contract. Bump it whenever
// either changes, so stored insights remain explainable.
const PromptVersion = "2026-11-21.1"

// Insight statuses.
const (
	StatusOK          = "ok"
	StatusInsufficient = "insufficient"
	StatusFailed      = "failed"
)

// Confidence values the prompt allows.
const (
	ConfidenceLow    = "low"
	ConfidenceMedium = "medium"
	ConfidenceHigh   = "high"
)

// Evidence points at something in the incident's own data. Exactly one of
// EventID or Field is set.
type Evidence struct {
	Kind    string     `json:"kind"` // event | field
	EventID *uuid.UUID `json:"event_id,omitempty"`
	Field   string     `json:"field,omitempty"`
	Note    string     `json:"note,omitempty"`
}

// Hypothesis is one candidate cause, ranked. Unsupported is set by the server,
// never by the model: it means a cited event id did not exist.
type Hypothesis struct {
	Rank        int        `json:"rank"`
	Cause       string     `json:"cause"`
	Confidence  string     `json:"confidence"`
	Evidence    []Evidence `json:"evidence"`
	FalsifiedBy string     `json:"falsified_by,omitempty"`
	Unsupported bool       `json:"unsupported,omitempty"`
}

// Result is the validated answer.
type Result struct {
	Sufficient bool         `json:"sufficient"`
	Assessment string       `json:"assessment"`
	Hypotheses []Hypothesis `json:"hypotheses"`
}

// Insight is a stored analysis.
type Insight struct {
	ID            uuid.UUID  `json:"id"`
	WorkspaceID   uuid.UUID  `json:"workspace_id"`
	IncidentID    uuid.UUID  `json:"incident_id"`
	Model         string     `json:"model"`
	PromptVersion string     `json:"prompt_version"`
	PayloadHash   string     `json:"payload_hash"`
	Status        string     `json:"status"`
	Result        Result     `json:"result"`
	InputTokens   int        `json:"input_tokens"`
	OutputTokens  int        `json:"output_tokens"`
	CreatedBy     *uuid.UUID `json:"created_by"`
	CreatedAt     time.Time  `json:"created_at"`
}
