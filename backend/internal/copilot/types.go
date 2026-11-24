// Package copilot runs an evidence-gathering investigation of an incident and
// stores the cited result. This file defines the stored shape; the agent loop,
// tool registry, and HTTP surface live in later tasks.
package copilot

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Status is the lifecycle state of one investigation row.
type Status string

// Investigation statuses. A row starts queued, moves to running, and ends in
// exactly one terminal state.
const (
	StatusQueued       Status = "queued"
	StatusRunning      Status = "running"
	StatusOK           Status = "ok"
	StatusInsufficient Status = "insufficient"
	StatusIncomplete   Status = "incomplete"
	StatusFailed       Status = "failed"
)

// Evidence points at a value inside a stored tool result.
type Evidence struct {
	ToolCallID string          `json:"tool_call_id"`
	Pointer    string          `json:"pointer"`
	Value      json.RawMessage `json:"value"`
	Note       string          `json:"note,omitempty"`
}

// Hypothesis is one candidate cause, ranked. Unverified is set by the server
// when the cited evidence could not be checked, never by the model.
type Hypothesis struct {
	Rank        int        `json:"rank"`
	Cause       string     `json:"cause"`
	Confidence  string     `json:"confidence"`
	Evidence    []Evidence `json:"evidence"`
	FalsifiedBy string     `json:"falsified_by,omitempty"`
	Unverified  bool       `json:"unverified,omitempty"`
}

// Result is the validated answer stored for a terminal investigation.
type Result struct {
	Sufficient bool         `json:"sufficient"`
	Assessment string       `json:"assessment"`
	Hypotheses []Hypothesis `json:"hypotheses"`
	NextChecks []string     `json:"next_checks,omitempty"`
}

// Step is one model turn in the trace.
type Step struct {
	Index       int    `json:"index"`
	Tool        string `json:"tool,omitempty"`
	ArgsSummary string `json:"args_summary,omitempty"`
	Status      string `json:"status"`
	DurationMS  int    `json:"duration_ms"`
}

// ToolCallRecord is one executed tool call in the trace. Result holds the
// redacted result, or a hash and a truncated marker when the cap was reached.
type ToolCallRecord struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	ArgsSummary string          `json:"args_summary,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	IsError     bool            `json:"is_error,omitempty"`
	DurationMS  int             `json:"duration_ms"`
}

// Trace is the stored audit trail of a run.
type Trace struct {
	Steps []Step           `json:"steps"`
	Calls []ToolCallRecord `json:"calls"`
}

// Investigation is one stored run for an incident.
type Investigation struct {
	ID            uuid.UUID  `json:"id"`
	WorkspaceID   uuid.UUID  `json:"workspace_id"`
	IncidentID    uuid.UUID  `json:"incident_id"`
	Status        Status     `json:"status"`
	Model         string     `json:"model"`
	PromptVersion string     `json:"prompt_version"`
	SeedHash      string     `json:"seed_hash"`
	Result        *Result    `json:"result,omitempty"`
	Trace         Trace      `json:"trace"`
	Attempts      int        `json:"attempts"`
	ToolCalls     int        `json:"tool_calls"`
	InputTokens   int        `json:"input_tokens"`
	OutputTokens  int        `json:"output_tokens"`
	DurationMS    int        `json:"duration_ms"`
	ErrorCode     string     `json:"error_code,omitempty"`
	CreatedBy     *uuid.UUID `json:"created_by,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}
