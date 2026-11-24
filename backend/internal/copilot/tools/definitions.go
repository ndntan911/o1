// Package tools is the read-only tool layer shared by the internal copilot
// agent and the MCP adapter. Every tool reads through the existing domain
// services, so authorization and tenant isolation stay in one place, and every
// result is projected through internal/evidence so secrets cannot leak.
package tools

import (
	"encoding/json"
	"time"

	"o1/internal/workspaces"
)

// Spec is one tool definition. MinRole is the MCP tool-list filter; the source
// services still enforce authorization on every call.
type Spec struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	MinRole     workspaces.Role
}

// Meta describes a tool result envelope. Count and aggregate values describe
// the full requested window, Total is the number of items before truncation,
// and Returned is the number of items in the returned array.
type Meta struct {
	AsOf      time.Time `json:"as_of"`
	Count     int       `json:"count"`
	Returned  int       `json:"returned"`
	Total     int       `json:"total"`
	Truncated bool      `json:"truncated"`
}

// Result is a tool result. Success marshals as the {data, meta} envelope; a
// domain failure sets IsError and ErrorCode.
type Result struct {
	Data      any    `json:"data"`
	Meta      Meta   `json:"meta"`
	IsError   bool   `json:"is_error,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
}

// specs is the registry's fixed tool list, in spec order. Descriptions double
// as model guidance, so they state the scope and the redaction boundary.
var specs = []Spec{
	{
		Name:        "o1_list_monitors",
		Description: "List the workspace's HTTP monitors with current health and any active incident id. Read-only; never returns the target URL, headers, body, or query.",
		MinRole:     workspaces.RoleViewer,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"additionalProperties": false,
			"properties": {
				"limit": {
					"type": "integer",
					"minimum": 1,
					"maximum": 50,
					"description": "Maximum monitors to return, newest first. Defaults to 50."
				}
			}
		}`),
	},
	{
		Name:        "o1_get_monitor",
		Description: "Read one workspace monitor's probe configuration, current health, and active incident id. Read-only; never returns the target URL, headers, body, or query.",
		MinRole:     workspaces.RoleViewer,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"additionalProperties": false,
			"required": ["monitor_id"],
			"properties": {
				"monitor_id": {
					"type": "string",
					"format": "uuid",
					"description": "Monitor id from o1_list_monitors."
				}
			}
		}`),
	},
	{
		Name:        "o1_list_incidents",
		Description: "List incidents in the workspace, newest first. Defaults to active incidents. Read-only; never returns notification state or recipients.",
		MinRole:     workspaces.RoleViewer,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"additionalProperties": false,
			"properties": {
				"monitor_id": {
					"type": "string",
					"format": "uuid",
					"description": "Optional monitor id to restrict the list."
				},
				"status": {
					"type": "string",
					"enum": ["active", "open", "acknowledged", "resolved", "all"],
					"description": "Optional status filter. Defaults to active."
				},
				"limit": {
					"type": "integer",
					"minimum": 1,
					"maximum": 20,
					"description": "Maximum incidents to return. Defaults to 20."
				}
			}
		}`),
	},
	{
		Name:        "o1_get_incident",
		Description: "Read one incident and its allowlisted timeline events. Read-only; never returns actor emails or incident detail outside the allowlist.",
		MinRole:     workspaces.RoleViewer,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"additionalProperties": false,
			"required": ["incident_id"],
			"properties": {
				"incident_id": {
					"type": "string",
					"format": "uuid",
					"description": "Incident id from o1_list_incidents."
				}
			}
		}`),
	},
	{
		Name:        "o1_get_recent_checks",
		Description: "Read a monitor's recent probe checks with failure classes and full-window aggregates. Read-only; never returns raw probe errors, response bodies, or the target URL.",
		MinRole:     workspaces.RoleViewer,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"additionalProperties": false,
			"required": ["monitor_id"],
			"properties": {
				"monitor_id": {
					"type": "string",
					"format": "uuid",
					"description": "Monitor id from o1_list_monitors."
				},
				"hours": {
					"type": "integer",
					"minimum": 1,
					"maximum": 168,
					"description": "Lookback window in hours. Defaults to 24."
				}
			}
		}`),
	},
	{
		Name:        "o1_get_alert_deliveries",
		Description: "Read member-safe alert delivery outcomes for one incident: channel kind, event, status, attempts, and timestamps. Member-only; never returns recipients, channel names, or error text.",
		MinRole:     workspaces.RoleMember,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"additionalProperties": false,
			"required": ["incident_id"],
			"properties": {
				"incident_id": {
					"type": "string",
					"format": "uuid",
					"description": "Incident id from o1_list_incidents."
				}
			}
		}`),
	},
}

// Specs returns the six tool definitions in spec order. The returned slice is a
// copy so callers cannot reorder or mutate the registry's definitions.
func Specs() []Spec {
	out := make([]Spec, len(specs))
	copy(out, specs)
	return out
}

// specFor looks up one definition by name.
func specFor(name string) (Spec, bool) {
	for _, spec := range specs {
		if spec.Name == name {
			return spec, true
		}
	}
	return Spec{}, false
}
