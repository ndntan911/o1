package incidents

import (
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"

	"o1/internal/email"
)

// BuildAlertMessage renders one incident alert email. It only uses data that is
// safe to expose: workspace/monitor names, status, the already-redacted URL,
// the incident start time, and the dashboard link.
func BuildAlertMessage(in AlertEmail) email.Message {
	workspace := inlineValue(in.WorkspaceName)
	if workspace == "" {
		workspace = "Workspace"
	}
	monitor := inlineValue(in.MonitorName)
	if monitor == "" {
		monitor = "Monitor"
	}
	monitorURL := inlineValue(in.MonitorURL)
	started := in.StartedAt.UTC().Format(time.RFC3339)
	link := strings.TrimRight(in.BaseURL, "/") + "/w/" + url.PathEscape(in.WorkspaceSlug) +
		"/incidents/" + in.IncidentID.String()

	status := "down"
	switch in.Event {
	case "reminder":
		status = "still down"
	case "resolved":
		status = "recovered"
	}

	subject := alertSubject(in.Event, monitor)

	var text strings.Builder
	fmt.Fprintf(&text, "Workspace: %s\n", workspace)
	fmt.Fprintf(&text, "Monitor: %s\n", monitor)
	fmt.Fprintf(&text, "Status: %s\n", status)
	fmt.Fprintf(&text, "URL: %s\n", monitorURL)
	fmt.Fprintf(&text, "Incident started: %s\n", started)
	if in.Event == "reminder" {
		fmt.Fprintf(&text, "Reminder: %d\n", in.Sequence)
	}
	fmt.Fprintf(&text, "\nView the incident: %s\n", link)
	if in.Event == "reminder" {
		fmt.Fprintf(&text, "\nAcknowledgement stops reminder emails, not probes.\n")
	}

	var body strings.Builder
	fmt.Fprintf(&body, "<p><strong>%s</strong> / <strong>%s</strong> status: %s.</p>\n",
		html.EscapeString(workspace), html.EscapeString(monitor), html.EscapeString(status))
	fmt.Fprintf(&body, "<p>URL: %s<br>\nIncident started: %s</p>\n",
		html.EscapeString(monitorURL), html.EscapeString(started))
	if in.Event == "reminder" {
		fmt.Fprintf(&body, "<p>Reminder: %d</p>\n", in.Sequence)
	}
	fmt.Fprintf(&body, "<p><a href=\"%s\">View the incident</a></p>\n", html.EscapeString(link))
	if in.Event == "reminder" {
		fmt.Fprintf(&body, "<p>Acknowledgement stops reminder emails, not probes.</p>\n")
	}

	return email.Message{
		To:      inlineValue(in.Recipient),
		Subject: subject,
		Text:    text.String(),
		HTML:    body.String(),
	}
}

func alertSubject(event, monitor string) string {
	switch event {
	case "opened":
		return fmt.Sprintf("[o1] %s is down", monitor)
	case "reminder":
		return fmt.Sprintf("[o1] Reminder: %s is still down", monitor)
	case "resolved":
		return fmt.Sprintf("[o1] %s recovered", monitor)
	default:
		return fmt.Sprintf("[o1] %s alert", monitor)
	}
}

// inlineValue strips CR/LF from a header or display value so user-controlled
// names cannot inject headers or split a line.
func inlineValue(value string) string {
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", "")
	return strings.TrimSpace(value)
}
