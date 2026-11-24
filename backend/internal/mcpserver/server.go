// Package mcpserver exposes the read-only o1 tool registry over the Model
// Context Protocol. It authenticates API keys, scopes every call to the key's
// workspace, and registers only the tools the key's role may use.
package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"o1/internal/audit"
	"o1/internal/auth"
	"o1/internal/copilot/tools"
	"o1/internal/db/sqlc"
	"o1/internal/httpx"
	"o1/internal/observability"
	"o1/internal/workspaces"
)

const (
	implementationName = "o1"
	// defaultVersion is reported when the caller does not supply a release.
	defaultVersion = "1"
	// auditKindToolCalled is the bounded security-event kind for MCP tool use.
	auditKindToolCalled = "mcp.tool_called"
	// instructions is sent during discovery and initialize. It states the
	// read-only, workspace-scoped contract and where callers find ids.
	instructions = "The o1 MCP surface is read-only and scoped to the API key's workspace. " +
		"Every tool returns only data the key's role may read. " +
		"Get monitor ids from o1_list_monitors and incident ids from o1_list_incidents; never guess ids."
)

// Options configures an MCP Server.
type Options struct {
	Auth           *auth.Service
	Registry       *tools.Registry
	Queries        *sqlc.Queries
	AllowedOrigins []string
	Version        string
	Logger         *slog.Logger
	Metrics        *observability.Metrics
}

// Server serves POST /mcp with per-key, stateless MCP sessions.
type Server struct {
	auth           *auth.Service
	registry       *tools.Registry
	queries        *sqlc.Queries
	allowedOrigins map[string]struct{}
	version        string
	logger         *slog.Logger
	metrics        *observability.Metrics
}

// New builds an MCP Server. The returned Server has no sessions; every request
// gets a fresh stateless session built for the authenticated key.
func New(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	version := opts.Version
	if version == "" {
		version = defaultVersion
	}
	origins := make(map[string]struct{}, len(opts.AllowedOrigins))
	for _, origin := range opts.AllowedOrigins {
		if normalized := allowedOrigin(origin); normalized != "" {
			origins[normalized] = struct{}{}
		}
	}
	return &Server{
		auth:           opts.Auth,
		registry:       opts.Registry,
		queries:        opts.Queries,
		allowedOrigins: origins,
		version:        version,
		logger:         logger,
		metrics:        opts.Metrics,
	}
}

// Handler returns the HTTP handler for POST /mcp. It accepts only a Bearer API
// key, checks Origin, rate-limits per key, and forwards to the SDK's stateless
// Streamable HTTP transport.
func (s *Server) Handler() http.Handler {
	if s.auth == nil || s.registry == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			httpx.WriteProblem(w, httpx.Problem{
				Type:   "about:blank",
				Title:  "Not Implemented",
				Status: http.StatusNotImplemented,
				Code:   httpx.CodeNotImplemented,
			})
		})
	}

	sdk := mcp.NewStreamableHTTPHandler(s.serverForRequest, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		Logger:       s.logger,
	})
	limited := httpx.KeyedRateLimit(auth.APIKeyRateKey, ratePerSecond, rateBurst)(sdk)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			s.logger.Warn("mcp authentication failed", "prefix", "", "reason", "missing_bearer")
			writeUnauthorized(w)
			return
		}
		principal, key, err := s.auth.AuthenticateAPIKey(r.Context(), raw)
		if err != nil {
			s.logger.Warn("mcp authentication failed",
				"prefix", keyPrefixForLog(raw), "reason", "invalid_key")
			writeUnauthorized(w)
			return
		}
		if !s.originAllowed(r) {
			s.logger.Warn("mcp origin rejected",
				"key_prefix", key.Prefix, "key_id", key.ID)
			writeForbidden(w)
			return
		}
		s.logger.Debug("mcp authenticated", "key_prefix", key.Prefix, "key_id", key.ID)

		ctx := auth.ContextWithPrincipal(r.Context(), principal)
		ctx = auth.ContextWithAPIKey(ctx, key)
		limited.ServeHTTP(w, r.WithContext(ctx))
	})
}

// serverForRequest builds the stateless MCP server for one authenticated key.
// The SDK calls this for every request, so the tool set always matches the
// current key role without any shared mutable state.
func (s *Server) serverForRequest(r *http.Request) *mcp.Server {
	key, ok := auth.APIKeyFrom(r.Context())
	if !ok {
		return nil
	}
	role, err := workspaces.ParseRole(key.Role)
	if err != nil {
		s.logger.Warn("mcp key role invalid", "key_prefix", key.Prefix, "key_id", key.ID)
		return nil
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    implementationName,
		Version: s.version,
	}, &mcp.ServerOptions{
		Instructions: instructions,
		Logger:       s.logger,
	})
	for _, spec := range s.registry.Specs() {
		if !role.AtLeast(spec.MinRole) {
			continue
		}
		server.AddTool(&mcp.Tool{
			Name:        spec.Name,
			Description: spec.Description,
			InputSchema: spec.InputSchema,
		}, s.toolHandler(spec.Name))
	}
	return server
}

// toolHandler adapts one registry spec to an MCP tool handler.
func (s *Server) toolHandler(name string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return s.callTool(ctx, name, req), nil
	}
}

// callTool executes one registry tool and records its bounded audit event and
// metrics. Every domain failure is returned as a tool error, never as a
// protocol error, so the model can see and correct it.
func (s *Server) callTool(ctx context.Context, name string, req *mcp.CallToolRequest) *mcp.CallToolResult {
	start := time.Now()
	key, keyOK := auth.APIKeyFrom(ctx)
	principal, principalOK := auth.PrincipalFrom(ctx)
	if !keyOK || !principalOK {
		s.observe(name, "error", start)
		return textResult(tools.Result{
			IsError:   true,
			ErrorCode: "forbidden",
			Meta:      tools.Meta{AsOf: time.Now().UTC()},
		})
	}

	var args json.RawMessage
	if req != nil && req.Params != nil {
		args = req.Params.Arguments
	}

	result, err := s.registry.Call(ctx, key.WorkspaceID, name, args)
	status := "ok"
	switch {
	case err != nil:
		// Registry programming errors are never copied into the result; only
		// the bounded internal code survives.
		result = tools.Result{
			IsError:   true,
			ErrorCode: "internal",
			Meta:      tools.Meta{AsOf: time.Now().UTC()},
		}
		status = "error"
	case result.IsError:
		status = "error"
	}

	s.recordToolCalled(ctx, key, principal, name, status)
	s.observe(name, status, start)
	return textResult(result)
}

// recordToolCalled writes mcp.tool_called best-effort. The detail carries only
// the tool name and bounded status; the key id is a column, never the secret.
func (s *Server) recordToolCalled(ctx context.Context, key auth.APIKeyPrincipal, principal auth.Principal, name, status string) {
	if s.queries == nil {
		return
	}
	workspaceID := key.WorkspaceID
	userID := principal.UserID
	keyID := key.ID
	audit.RecordBestEffort(ctx, s.queries, audit.Event{
		WorkspaceID:   &workspaceID,
		ActorUserID:   &userID,
		ActorAPIKeyID: &keyID,
		Kind:          auditKindToolCalled,
		Detail:        map[string]any{"tool": name, "status": status},
	})
}

// observe records one bounded tool call.
func (s *Server) observe(tool, status string, start time.Time) {
	s.metrics.ObserveMCPTool(tool, status, time.Since(start))
}

// textResult serializes a registry result as the tool's text content. It
// preserves IsError so clients can distinguish domain failures from success.
func textResult(result tools.Result) *mcp.CallToolResult {
	raw, err := json.Marshal(result)
	if err != nil {
		raw = []byte(`{"is_error":true,"error_code":"internal"}`)
		result.IsError = true
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}},
		IsError: result.IsError,
	}
}
