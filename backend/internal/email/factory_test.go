package email_test

import (
	"strings"
	"testing"

	"o1/internal/email"
	"o1/internal/testutil"
)

func TestNewSender_SelectsProvider(t *testing.T) {
	defaults := testutil.TestConfig(t)
	defaults.EmailProvider = "mailpit"
	defaults.EmailFrom = "o1@example.com"
	sender, err := email.NewSender(defaults)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sender.(*email.RetrySender); !ok {
		t.Fatalf("sender = %T, want retry wrapper", sender)
	}

	defaults.EmailProvider = "resend"
	defaults.ResendAPIKey = "re_test_key"
	defaults.ResendAPIURL = "https://api.resend.com"
	if _, err := email.NewSender(defaults); err != nil {
		t.Fatalf("resend factory: %v", err)
	}

	defaults.EmailProvider = "bogus"
	if _, err := email.NewSender(defaults); err == nil {
		t.Fatal("invalid provider accepted")
	}
}

func TestNewSender_ResolvesEmptyProvider(t *testing.T) {
	cfg := testutil.TestConfig(t) // Env is "test".
	cfg.EmailFrom = "o1@example.com"
	cfg.EmailProvider = ""

	sender, err := email.NewSender(cfg)
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	if _, ok := sender.(*email.RetrySender); !ok {
		t.Fatalf("sender = %T, want retry wrapper", sender)
	}
}

func TestNewSender_RejectsMissingConfiguration(t *testing.T) {
	t.Run("mailpit without smtp address", func(t *testing.T) {
		cfg := testutil.TestConfig(t)
		cfg.EmailProvider = "mailpit"
		cfg.SMTPAddr = ""

		if _, err := email.NewSender(cfg); err == nil {
			t.Fatal("NewSender accepted mailpit without an SMTP address")
		}
	})

	t.Run("mailpit without from", func(t *testing.T) {
		cfg := testutil.TestConfig(t)
		cfg.EmailProvider = "mailpit"
		cfg.EmailFrom = ""

		if _, err := email.NewSender(cfg); err == nil {
			t.Fatal("NewSender accepted mailpit without a from address")
		}
	})

	t.Run("resend without api key", func(t *testing.T) {
		cfg := testutil.TestConfig(t)
		cfg.EmailProvider = "resend"
		cfg.ResendAPIKey = ""

		if _, err := email.NewSender(cfg); err == nil {
			t.Fatal("NewSender accepted resend without an API key")
		}
	})

	t.Run("production mailpit", func(t *testing.T) {
		cfg := testutil.TestConfig(t)
		cfg.Env = "production"
		cfg.EmailProvider = "mailpit"

		if _, err := email.NewSender(cfg); err == nil {
			t.Fatal("NewSender accepted mailpit in production")
		}
	})

	t.Run("error does not echo the api key", func(t *testing.T) {
		cfg := testutil.TestConfig(t)
		cfg.EmailProvider = "resend"
		cfg.ResendAPIKey = "re_super_secret"
		cfg.EmailFrom = "not-an-email"

		_, err := email.NewSender(cfg)
		if err == nil {
			t.Fatal("NewSender accepted a malformed from address")
		}
		if strings.Contains(err.Error(), "re_super_secret") {
			t.Fatalf("error leaks the API key: %v", err)
		}
	})
}

func TestNewSender_WrapsEveryProviderInRetry(t *testing.T) {
	cfg := testutil.TestConfig(t)
	cfg.EmailProvider = "resend"
	cfg.ResendAPIKey = "re_test_key"
	cfg.ResendAPIURL = "https://api.resend.com"

	sender, err := email.NewSender(cfg)
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	if _, ok := sender.(*email.RetrySender); !ok {
		t.Fatalf("sender = %T, want *email.RetrySender", sender)
	}
}
