package monitors

import (
	"net/http"
	"time"

	"o1/internal/httpx"
)

const (
	defaultResultsLimit = 100
	maxResultsLimit     = 500
)

// resultsResponse wraps a page of raw probe results.
type resultsResponse struct {
	Results    []ProbeResult `json:"results"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

// ResultsHandler returns GET /workspaces/{workspaceID}/monitors/{monitorID}/results.
func ResultsHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !serviceAvailable(w, svc) {
			return
		}
		principal, workspaceID, monitorID, ok := requireIDs(w, r)
		if !ok {
			return
		}
		window, ok := parseWindow(w, r)
		if !ok {
			return
		}
		limit, ok := parseLimit(w, r, defaultResultsLimit, maxResultsLimit)
		if !ok {
			return
		}
		cursor, ok := parseCursor(w, r)
		if !ok {
			return
		}
		results, err := svc.Results(r.Context(), principal.UserID, workspaceID, monitorID, window, cursor, limit)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		resp := resultsResponse{Results: results}
		if len(results) == limit {
			last := results[len(results)-1]
			resp.NextCursor = httpx.EncodeCursor(httpx.PageCursor{Time: last.CheckedAt, ID: last.ID})
		}
		httpx.WriteJSON(w, http.StatusOK, resp)
	}
}

// SummaryHandler returns GET /workspaces/{workspaceID}/monitors/{monitorID}/summary.
func SummaryHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !serviceAvailable(w, svc) {
			return
		}
		principal, workspaceID, monitorID, ok := requireIDs(w, r)
		if !ok {
			return
		}
		window, ok := parseWindow(w, r)
		if !ok {
			return
		}
		summary, err := svc.Summary(r.Context(), principal.UserID, workspaceID, monitorID, window)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, summary)
	}
}

// parseWindow validates the window query parameter; the default is 24h.
func parseWindow(w http.ResponseWriter, r *http.Request) (time.Duration, bool) {
	switch r.URL.Query().Get("window") {
	case "":
		return ResultsWindow24h, true
	case "1h":
		return ResultsWindow1h, true
	case "6h":
		return ResultsWindow6h, true
	case "24h":
		return ResultsWindow24h, true
	case "7d":
		return ResultsWindow7d, true
	default:
		writeFieldsProblem(w, map[string]string{"window": "window must be one of 1h, 6h, 24h, 7d"})
		return 0, false
	}
}
