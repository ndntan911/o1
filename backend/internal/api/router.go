// Package api wires the HTTP routes and the middleware chain for the API.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"o1/internal/admin"
	"o1/internal/alerts"
	"o1/internal/auth"
	"o1/internal/config"
	"o1/internal/db/sqlc"
	"o1/internal/email"
	"o1/internal/httpx"
	"o1/internal/incidents"
	"o1/internal/monitors"
	"o1/internal/probe"
	"o1/internal/secretbox"
	"o1/internal/workspaces"
)

// Dependencies carries the services the router needs. The services are
// optional: when nil the router builds them from Pool, Config, and
// EmailSender. main.go passes explicit instances so it can close the auth
// service before the database pool.
type Dependencies struct {
	Logger           *slog.Logger
	Pool             *pgxpool.Pool
	Config           config.Config
	EmailSender      email.Sender
	AuthService      *auth.Service
	WorkspaceService *workspaces.Service
	MonitorService   *monitors.Service
	IncidentService  *incidents.Service
	AlertService     *alerts.Service
	AdminService     *admin.Service
}

// authRatePerSecond and authRateBurst implement 10 requests per minute for
// the credential-handling endpoints.
const (
	authRatePerSecond = 10.0 / 60.0
	authRateBurst     = 10
)

// maxBodyBytes limits the size of request bodies (1 MiB).
const maxBodyBytes int64 = 1 << 20

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// withRequestID assigns an X-Request-Id to every request and stores it in
// the request context.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			var buf [16]byte
			_, _ = rand.Read(buf[:])
			id = hex.EncodeToString(buf[:])
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

type requestIDKey struct{}

// RequestID returns the request ID assigned to req, if any.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// withSecurityHeaders sets conservative default response headers.
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// withBodyLimit rejects request bodies larger than 1 MiB.
func withBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// requestLogging emits one structured log line per request.
func requestLogging(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration", time.Since(start).String(),
				"request_id", RequestID(r.Context()),
			}
			logger.Log(r.Context(), slog.LevelInfo, "http request", attrs...)
		})
	}
}

// recoverPanics converts panics into a 500 problem response instead of
// crashing the server.
func recoverPanics(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Log(r.Context(), slog.LevelError, "panic recovered", "panic", rec, "request_id", RequestID(r.Context()))
					httpx.WriteProblem(w, httpx.Problem{
						Type:   "about:blank",
						Title:  "Internal Server Error",
						Status: http.StatusInternalServerError,
						Code:   "internal_error",
					})
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// NewRouter builds the root HTTP handler.
func NewRouter(deps Dependencies) http.Handler {
	logger := deps.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	authSvc := deps.AuthService
	if authSvc == nil {
		authSvc = auth.NewService(deps.Pool, sqlc.New(deps.Pool), deps.EmailSender, deps.Config)
	}
	wsSvc := deps.WorkspaceService
	if wsSvc == nil {
		wsSvc = workspaces.NewService(deps.Pool, sqlc.New(deps.Pool))
	}
	monitorSvc := deps.MonitorService
	if monitorSvc == nil && deps.Pool != nil && len(deps.Config.EncryptionKey) == 32 {
		if box, err := secretbox.New(deps.Config.EncryptionKey); err == nil {
			monitorSvc = monitors.NewService(
				deps.Pool,
				sqlc.New(deps.Pool),
				wsSvc,
				box,
				probe.NewGuard(probe.PolicyFromConfig(deps.Config)),
				deps.Config,
			)
		}
	}
	adminSvc := deps.AdminService
	if adminSvc == nil && deps.Pool != nil {
		adminSvc = admin.NewService(deps.Pool, sqlc.New(deps.Pool))
	}
	incidentSvc := deps.IncidentService
	if incidentSvc == nil && deps.Pool != nil {
		incidentSvc = incidents.NewService(deps.Pool, sqlc.New(deps.Pool), wsSvc)
	}
	alertSvc := deps.AlertService
	if alertSvc == nil && deps.Pool != nil && len(deps.Config.EncryptionKey) == 32 {
		if box, err := secretbox.New(deps.Config.EncryptionKey); err == nil {
			guard := probe.NewGuard(probe.PolicyFromConfig(deps.Config))
			alertSvc = alerts.NewService(
				deps.Pool,
				sqlc.New(deps.Pool),
				wsSvc,
				box,
				guard,
				alerts.NewNotifier(guard, ""),
				deps.EmailSender,
				deps.Config.AppBaseURL,
			)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", httpx.HandleHealthz)
	mux.HandleFunc("GET /readyz", readyzHandler(deps.Pool))

	requireSession := auth.RequireSession(authSvc)
	authLimiter := httpx.RateLimit(authRatePerSecond, authRateBurst, true)

	mux.Handle("POST /api/v1/auth/register", authLimiter(http.HandlerFunc(auth.RegisterHandler(authSvc))))
	mux.Handle("POST /api/v1/auth/verify-email", authLimiter(http.HandlerFunc(auth.VerifyEmailHandler(authSvc))))
	mux.Handle("POST /api/v1/auth/verify-email/resend", authLimiter(http.HandlerFunc(auth.ResendVerificationHandler(authSvc))))
	mux.Handle("POST /api/v1/auth/login", authLimiter(http.HandlerFunc(auth.LoginHandler(authSvc, deps.Config))))
	mux.Handle("POST /api/v1/auth/logout", requireSession(http.HandlerFunc(auth.LogoutHandler(authSvc, deps.Config))))
	mux.Handle("POST /api/v1/auth/password/forgot", authLimiter(http.HandlerFunc(auth.ForgotPasswordHandler(authSvc))))
	mux.Handle("POST /api/v1/auth/password/reset", authLimiter(http.HandlerFunc(auth.ResetPasswordHandler(authSvc))))

	mux.Handle("GET /api/v1/me", requireSession(http.HandlerFunc(MeHandler(wsSvc))))

	mux.Handle("GET /api/v1/workspaces", requireSession(http.HandlerFunc(workspaces.ListHandler(wsSvc))))
	mux.Handle("POST /api/v1/workspaces", requireSession(http.HandlerFunc(workspaces.CreateHandler(wsSvc))))
	mux.Handle("GET /api/v1/workspaces/{workspaceID}", requireSession(http.HandlerFunc(workspaces.GetHandler(wsSvc))))
	mux.Handle("GET /api/v1/workspaces/{workspaceID}/members", requireSession(http.HandlerFunc(workspaces.MembersHandler(wsSvc))))
	mux.Handle("POST /api/v1/workspaces/{workspaceID}/members", requireSession(http.HandlerFunc(workspaces.AddMemberHandler(wsSvc))))

	mux.Handle("POST /api/v1/workspaces/{workspaceID}/monitors", requireSession(http.HandlerFunc(monitors.CreateHandler(monitorSvc))))
	mux.Handle("GET /api/v1/workspaces/{workspaceID}/monitors", requireSession(http.HandlerFunc(monitors.ListHandler(monitorSvc))))
	mux.Handle("GET /api/v1/workspaces/{workspaceID}/monitors/{monitorID}", requireSession(http.HandlerFunc(monitors.GetHandler(monitorSvc))))
	mux.Handle("PATCH /api/v1/workspaces/{workspaceID}/monitors/{monitorID}", requireSession(http.HandlerFunc(monitors.UpdateHandler(monitorSvc))))
	mux.Handle("DELETE /api/v1/workspaces/{workspaceID}/monitors/{monitorID}", requireSession(http.HandlerFunc(monitors.DeleteHandler(monitorSvc))))
	mux.Handle("POST /api/v1/workspaces/{workspaceID}/monitors/{monitorID}/pause", requireSession(http.HandlerFunc(monitors.PauseHandler(monitorSvc))))
	mux.Handle("POST /api/v1/workspaces/{workspaceID}/monitors/{monitorID}/resume", requireSession(http.HandlerFunc(monitors.ResumeHandler(monitorSvc))))
	mux.Handle("GET /api/v1/workspaces/{workspaceID}/monitors/{monitorID}/results", requireSession(http.HandlerFunc(monitors.ResultsHandler(monitorSvc))))
	mux.Handle("GET /api/v1/workspaces/{workspaceID}/monitors/{monitorID}/summary", requireSession(http.HandlerFunc(monitors.SummaryHandler(monitorSvc))))

	mux.Handle("GET /api/v1/workspaces/{workspaceID}/incidents", requireSession(http.HandlerFunc(incidents.ListHandler(incidentSvc))))
	mux.Handle("GET /api/v1/workspaces/{workspaceID}/incidents/{incidentID}", requireSession(http.HandlerFunc(incidents.GetHandler(incidentSvc))))
	mux.Handle("POST /api/v1/workspaces/{workspaceID}/incidents/{incidentID}/acknowledge", requireSession(http.HandlerFunc(incidents.AcknowledgeHandler(incidentSvc))))
	mux.Handle("GET /api/v1/workspaces/{workspaceID}/incidents/{incidentID}/deliveries", requireSession(http.HandlerFunc(incidents.DeliveriesHandler(incidentSvc))))

	mux.Handle("GET /api/v1/workspaces/{workspaceID}/alert-channels", requireSession(http.HandlerFunc(alerts.ListChannelsHandler(alertSvc))))
	mux.Handle("POST /api/v1/workspaces/{workspaceID}/alert-channels", requireSession(http.HandlerFunc(alerts.CreateChannelHandler(alertSvc))))
	mux.Handle("PATCH /api/v1/workspaces/{workspaceID}/alert-channels/{channelID}", requireSession(http.HandlerFunc(alerts.UpdateChannelHandler(alertSvc))))
	mux.Handle("DELETE /api/v1/workspaces/{workspaceID}/alert-channels/{channelID}", requireSession(http.HandlerFunc(alerts.DeleteChannelHandler(alertSvc))))
	mux.Handle("PUT /api/v1/workspaces/{workspaceID}/alert-channels/email", requireSession(http.HandlerFunc(incidents.PutEmailChannelHandler(incidentSvc))))
	mux.Handle("GET /api/v1/workspaces/{workspaceID}/monitors/{monitorID}/alert-channel", requireSession(http.HandlerFunc(incidents.MonitorAlertChannelHandler(incidentSvc))))
	mux.Handle("PATCH /api/v1/workspaces/{workspaceID}/monitors/{monitorID}/alert-channel", requireSession(http.HandlerFunc(incidents.SetMonitorAlertChannelHandler(incidentSvc))))

	requireAdmin := auth.RequireAdmin
	mux.Handle("GET /api/v1/admin/workspaces", requireSession(requireAdmin(http.HandlerFunc(admin.WorkspacesHandler(adminSvc)))))
	mux.Handle("POST /api/v1/admin/workspaces/{workspaceID}/suspend", requireSession(requireAdmin(http.HandlerFunc(admin.SuspendHandler(adminSvc)))))
	mux.Handle("POST /api/v1/admin/workspaces/{workspaceID}/unsuspend", requireSession(requireAdmin(http.HandlerFunc(admin.UnsuspendHandler(adminSvc)))))
	mux.Handle("GET /api/v1/admin/monitors", requireSession(requireAdmin(http.HandlerFunc(admin.MonitorsHandler(adminSvc)))))
	mux.Handle("GET /api/v1/admin/security-events", requireSession(requireAdmin(http.HandlerFunc(admin.SecurityEventsHandler(adminSvc)))))

	var handler http.Handler = mux
	handler = withBodyLimit(handler)
	handler = httpx.CSRF(deps.Config.AllowedOrigins)(handler)
	handler = withSecurityHeaders(handler)
	handler = requestLogging(logger)(handler)
	handler = recoverPanics(logger)(handler)
	handler = withRequestID(handler)
	return handler
}

// readinessResponse is the JSON body of a successful GET /readyz.
type readinessResponse struct {
	Status string `json:"status"`
}

// readyzHandler pings the database with a short timeout. A missing or
// unreachable database yields 503 so orchestrators stop routing traffic here.
func readyzHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if pool == nil {
			writeNotReady(w)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			writeNotReady(w)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, readinessResponse{Status: "ok"})
	}
}

func writeNotReady(w http.ResponseWriter) {
	httpx.WriteProblem(w, httpx.Problem{
		Type:   "about:blank",
		Title:  "Service Unavailable",
		Status: http.StatusServiceUnavailable,
		Code:   "not_ready",
	})
}

// meUser is the public representation of the authenticated user.
type meUser struct {
	ID      uuid.UUID `json:"id"`
	Email   string    `json:"email"`
	IsAdmin bool      `json:"is_admin"`
}

// meWorkspace is a workspace as returned by GET /me.
type meWorkspace struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Slug string    `json:"slug"`
	Role string    `json:"role"`
}

// meResponse is the JSON body of GET /me.
type meResponse struct {
	User       meUser        `json:"user"`
	Workspaces []meWorkspace `json:"workspaces"`
}

// MeHandler returns the authenticated user together with every workspace they
// belong to.
func MeHandler(wsSvc *workspaces.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := auth.PrincipalFrom(r.Context())
		if !ok {
			httpx.WriteProblem(w, httpx.Problem{
				Type:   "about:blank",
				Title:  "Authentication required",
				Status: http.StatusUnauthorized,
				Code:   httpx.CodeUnauthenticated,
			})
			return
		}
		list, err := wsSvc.ListForUser(r.Context(), principal.UserID)
		if err != nil {
			httpx.WriteProblem(w, httpx.Problem{
				Type:   "about:blank",
				Title:  "Internal Server Error",
				Status: http.StatusInternalServerError,
				Code:   "internal_error",
			})
			return
		}
		out := make([]meWorkspace, 0, len(list))
		for _, ws := range list {
			out = append(out, meWorkspace{ID: ws.ID, Name: ws.Name, Slug: ws.Slug, Role: ws.Role.String()})
		}
		httpx.WriteJSON(w, http.StatusOK, meResponse{
			User:       meUser{ID: principal.UserID, Email: principal.Email, IsAdmin: principal.IsAdmin},
			Workspaces: out,
		})
	}
}
