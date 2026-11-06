package incidents

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testAlertEmail() AlertEmail {
	return AlertEmail{
		WorkspaceName: "Acme Corp",
		WorkspaceSlug: "acme-corp",
		MonitorName:   "API health",
		MonitorURL:    "https://example.com/health",
		Event:         "opened",
		Sequence:      0,
		IncidentID:    uuid.MustParse("018f2f5a-7d8e-7c31-9f3a-4b5c6d7e8f90"),
		StartedAt:     time.Date(2026, 11, 6, 10, 30, 0, 0, time.UTC),
		BaseURL:       "http://localhost:5173",
		Recipient:     "user@example.com",
	}
}

func TestBuildAlertMessageOpened(t *testing.T) {
	in := testAlertEmail()
	msg := BuildAlertMessage(in)

	if msg.To != in.Recipient {
		t.Fatalf("To = %q, want %q", msg.To, in.Recipient)
	}
	if !strings.Contains(msg.Subject, in.MonitorName) {
		t.Fatalf("subject %q does not contain monitor name", msg.Subject)
	}
	if !strings.Contains(msg.Subject, "down") {
		t.Fatalf("subject %q does not say down", msg.Subject)
	}
	link := "http://localhost:5173/w/acme-corp/incidents/" + in.IncidentID.String()
	for name, body := range map[string]string{"text": msg.Text, "html": msg.HTML} {
		for _, want := range []string{
			"Acme Corp",
			"API health",
			"https://example.com/health",
			link,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s body missing %q in:\n%s", name, want, body)
			}
		}
	}
}

func TestBuildAlertMessageReminderAndResolved(t *testing.T) {
	reminder := testAlertEmail()
	reminder.Event = "reminder"
	reminder.Sequence = 2
	msg := BuildAlertMessage(reminder)
	if !strings.Contains(msg.Subject, "Reminder") || !strings.Contains(msg.Subject, reminder.MonitorName) {
		t.Fatalf("reminder subject = %q", msg.Subject)
	}
	if !strings.Contains(msg.Text, "2") {
		t.Fatalf("reminder text missing sequence number:\n%s", msg.Text)
	}
	if !strings.Contains(msg.Text, "Acknowledgement stops reminder emails, not probes.") {
		t.Fatalf("reminder text missing acknowledgement note:\n%s", msg.Text)
	}

	resolved := testAlertEmail()
	resolved.Event = "resolved"
	msg = BuildAlertMessage(resolved)
	if !strings.Contains(msg.Subject, "recovered") || !strings.Contains(msg.Subject, resolved.MonitorName) {
		t.Fatalf("resolved subject = %q", msg.Subject)
	}
	if strings.Contains(msg.Subject, "\r") || strings.Contains(msg.Subject, "\n") {
		t.Fatalf("resolved subject contains a newline: %q", msg.Subject)
	}
}

func TestBuildAlertMessageStripsHeaderInjection(t *testing.T) {
	in := testAlertEmail()
	in.MonitorName = "API\r\nBcc: evil@example.com"
	msg := BuildAlertMessage(in)

	if strings.Contains(msg.Subject, "\r") || strings.Contains(msg.Subject, "\n") {
		t.Fatalf("subject contains a newline: %q", msg.Subject)
	}
	if !strings.Contains(msg.Subject, "API") {
		t.Fatalf("subject lost the monitor name: %q", msg.Subject)
	}
}

func TestBuildAlertMessageEscapesHTML(t *testing.T) {
	in := testAlertEmail()
	in.MonitorName = `<script>alert("boom")</script>`
	msg := BuildAlertMessage(in)

	if strings.Contains(msg.HTML, "<script>") {
		t.Fatalf("HTML body contains unescaped script tag:\n%s", msg.HTML)
	}
	if !strings.Contains(msg.HTML, "&lt;script&gt;") {
		t.Fatalf("HTML body does not contain escaped monitor name:\n%s", msg.HTML)
	}
}

func TestBuildAlertMessageOldBoundaryName(t *testing.T) {
	in := testAlertEmail()
	in.MonitorName = "o1-mail-boundary"
	msg := BuildAlertMessage(in)

	for name, body := range map[string]string{"text": msg.Text, "html": msg.HTML} {
		if !strings.Contains(body, "o1-mail-boundary") {
			t.Errorf("%s body dropped the monitor name:\n%s", name, body)
		}
	}
}
