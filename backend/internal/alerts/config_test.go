package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/google/uuid"

	"o1/internal/db/sqlc"
	"o1/internal/probe"
	"o1/internal/secretbox"
)

type staticResolver struct{ ips []string }

func (r staticResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	out := make([]net.IPAddr, 0, len(r.ips))
	for _, raw := range r.ips {
		out = append(out, net.IPAddr{IP: net.ParseIP(raw)})
	}
	return out, nil
}

func testGuard(ips ...string) *probe.Guard {
	return probe.NewGuardWithResolver(probe.Policy{
		AllowedPorts: map[int]struct{}{80: {}, 443: {}},
	}, staticResolver{ips: ips})
}

func configTestBox(t *testing.T) *secretbox.Box {
	t.Helper()
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("secretbox.New: %v", err)
	}
	return box
}

func configTestPtr[T any](v T) *T { return &v }

func TestSealCreateConfig_Validation(t *testing.T) {
	box, _ := secretbox.New(bytes.Repeat([]byte{7}, 32))
	public := testGuard("93.184.216.34")
	private := testGuard("127.0.0.1")

	cases := []struct {
		name    string
		kind    string
		in      CreateInput
		guard   *probe.Guard
		wantErr bool
		field   string
	}{
		{"webhook valid", KindWebhook, CreateInput{URL: "https://example.com/o1"}, public, false, ""},
		{"webhook private blocked", KindWebhook, CreateInput{URL: "https://internal.example/o1"}, private, true, "url"},
		{"webhook userinfo blocked", KindWebhook, CreateInput{URL: "https://u:p@example.com/o1"}, public, true, "url"},
		{"webhook fragment blocked", KindWebhook, CreateInput{URL: "https://example.com/o1#x"}, public, true, "url"},
		{"webhook short secret", KindWebhook, CreateInput{URL: "https://example.com/o1", SigningSecret: "short"}, public, true, "signing_secret"},
		{"webhook too long", KindWebhook, CreateInput{URL: "https://example.com/" + strings.Repeat("a", 2048)}, public, true, "url"},
		{"slack valid", KindSlack, CreateInput{URL: "https://hooks.slack.com/services/T/B/X"}, public, false, ""},
		{"slack wrong host", KindSlack, CreateInput{URL: "https://example.com/services/T/B/X"}, public, true, "url"},
		{"slack http rejected", KindSlack, CreateInput{URL: "http://hooks.slack.com/services/T/B/X"}, public, true, "url"},
		{"telegram valid", KindTelegram, CreateInput{BotToken: "123456:ABC_def-1", ChatID: "-1001234567890"}, public, false, ""},
		{"telegram bad token", KindTelegram, CreateInput{BotToken: "not-a-token", ChatID: "-100123"}, public, true, "bot_token"},
		{"telegram bad chat", KindTelegram, CreateInput{BotToken: "123456:ABC", ChatID: "room one"}, public, true, "chat_id"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := SealCreateConfig(box, tc.guard, tc.kind, tc.in)
			if tc.wantErr {
				var validation *ValidationError
				if !errors.As(err, &validation) || validation.Fields[tc.field] == "" {
					t.Fatalf("error = %v, want field %q", err, tc.field)
				}
				configTestAssertNoEcho(t, err.Error(), tc.in)
				return
			}
			if err != nil {
				t.Fatalf("SealCreateConfig: %v", err)
			}
		})
	}

	if _, err := SealCreateConfig(box, public, KindEmail, CreateInput{}); !errors.Is(err, ErrEmailChannel) {
		t.Fatalf("email kind error = %v, want ErrEmailChannel", err)
	}

	_, err := SealCreateConfig(box, public, "carrier-pigeon", CreateInput{})
	var validation *ValidationError
	if !errors.As(err, &validation) || validation.Fields["kind"] == "" {
		t.Fatalf("unknown kind error = %v, want kind field", err)
	}
}

func configTestAssertNoEcho(t *testing.T, message string, in CreateInput) {
	t.Helper()
	for _, value := range []string{in.URL, in.SigningSecret, in.BotToken, in.ChatID} {
		if len(value) >= 6 && strings.Contains(message, value) {
			t.Fatalf("error message %q echoes submitted value %q", message, value)
		}
	}
}

func TestConfig_RoundTripAndMerge(t *testing.T) {
	box := configTestBox(t)
	guard := testGuard("93.184.216.34")

	t.Run("webhook round trip", func(t *testing.T) {
		const (
			rawURL = "https://example.com/o1?token=plain"
			secret = "0123456789abcdef"
		)
		config, err := SealCreateConfig(box, guard, KindWebhook, CreateInput{
			URL: rawURL, SigningSecret: secret,
		})
		if err != nil {
			t.Fatalf("SealCreateConfig: %v", err)
		}
		if strings.Contains(string(config), rawURL) || strings.Contains(string(config), secret) {
			t.Fatalf("sealed config exposes plaintext: %s", config)
		}

		view, err := ChannelView(sqlc.AlertChannel{
			ID: uuid.New(), Kind: KindWebhook, Name: "Deploy hook", Config: config, Enabled: true,
		})
		if err != nil {
			t.Fatalf("ChannelView: %v", err)
		}
		if view.URLPreview != "https://example.com/…" {
			t.Fatalf("URLPreview = %q", view.URLPreview)
		}
		if !view.SigningSecretSet {
			t.Fatal("SigningSecretSet = false, want true")
		}
		encoded, err := json.Marshal(view)
		if err != nil {
			t.Fatalf("marshal view: %v", err)
		}
		for _, forbidden := range []string{rawURL, secret, "url_enc", "signing_secret_enc"} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("view JSON contains %q: %s", forbidden, encoded)
			}
		}

		target, err := OpenTarget(box, KindWebhook, config)
		if err != nil {
			t.Fatalf("OpenTarget: %v", err)
		}
		if target.Kind != KindWebhook || target.URL != rawURL || target.SigningSecret != secret {
			t.Fatalf("target = %+v", target)
		}
	})

	t.Run("slack round trip", func(t *testing.T) {
		const rawURL = "https://hooks.slack.com/services/T/B/X"
		config, err := SealCreateConfig(box, guard, KindSlack, CreateInput{URL: rawURL})
		if err != nil {
			t.Fatalf("SealCreateConfig: %v", err)
		}
		view, err := ChannelView(sqlc.AlertChannel{Kind: KindSlack, Config: config})
		if err != nil {
			t.Fatalf("ChannelView: %v", err)
		}
		if view.URLPreview != "https://hooks.slack.com/…" {
			t.Fatalf("URLPreview = %q", view.URLPreview)
		}
		if view.SigningSecretSet {
			t.Fatal("SigningSecretSet = true for Slack, want false")
		}
		target, err := OpenTarget(box, KindSlack, config)
		if err != nil {
			t.Fatalf("OpenTarget: %v", err)
		}
		if target.URL != rawURL {
			t.Fatalf("target.URL = %q", target.URL)
		}
	})

	t.Run("preview keeps explicit port", func(t *testing.T) {
		config, err := SealCreateConfig(box, guard, KindWebhook, CreateInput{URL: "https://example.com:443/o1"})
		if err != nil {
			t.Fatalf("SealCreateConfig: %v", err)
		}
		view, err := ChannelView(sqlc.AlertChannel{Kind: KindWebhook, Config: config})
		if err != nil {
			t.Fatalf("ChannelView: %v", err)
		}
		if view.URLPreview != "https://example.com:443/…" {
			t.Fatalf("URLPreview = %q", view.URLPreview)
		}
	})

	t.Run("telegram round trip", func(t *testing.T) {
		const (
			token  = "123456:ABC_def-1"
			chatID = "-1001234567890"
		)
		config, err := SealCreateConfig(box, guard, KindTelegram, CreateInput{BotToken: token, ChatID: chatID})
		if err != nil {
			t.Fatalf("SealCreateConfig: %v", err)
		}
		if strings.Contains(string(config), token) {
			t.Fatalf("sealed config exposes token: %s", config)
		}
		view, err := ChannelView(sqlc.AlertChannel{Kind: KindTelegram, Config: config, Enabled: true})
		if err != nil {
			t.Fatalf("ChannelView: %v", err)
		}
		if !view.BotTokenSet || view.ChatID != chatID {
			t.Fatalf("view = %+v", view)
		}
		encoded, err := json.Marshal(view)
		if err != nil {
			t.Fatalf("marshal view: %v", err)
		}
		if strings.Contains(string(encoded), token) || strings.Contains(string(encoded), "bot_token_enc") {
			t.Fatalf("view JSON exposes token: %s", encoded)
		}
		target, err := OpenTarget(box, KindTelegram, config)
		if err != nil {
			t.Fatalf("OpenTarget: %v", err)
		}
		if target.BotToken != token || target.ChatID != chatID {
			t.Fatalf("target = %+v", target)
		}
	})

	t.Run("email view and open", func(t *testing.T) {
		view, err := ChannelView(sqlc.AlertChannel{
			Kind:   KindEmail,
			Config: []byte(`{"recipients":["ops@example.com"]}`),
		})
		if err != nil {
			t.Fatalf("ChannelView: %v", err)
		}
		if len(view.Recipients) != 1 || view.Recipients[0] != "ops@example.com" {
			t.Fatalf("Recipients = %#v", view.Recipients)
		}

		empty, err := ChannelView(sqlc.AlertChannel{Kind: KindEmail, Config: []byte(`{}`)})
		if err != nil {
			t.Fatalf("ChannelView empty: %v", err)
		}
		if empty.Recipients == nil {
			t.Fatal("Recipients = nil, want non-nil slice")
		}

		if _, err := OpenTarget(box, KindEmail, []byte(`{"recipients":["ops@example.com"]}`)); !errors.Is(err, ErrInvalidChannelConfig) {
			t.Fatalf("OpenTarget(email) error = %v, want ErrInvalidChannelConfig", err)
		}
	})

	t.Run("webhook merge keeps rotates and clears secret", func(t *testing.T) {
		const (
			originalURL    = "https://example.com/o1"
			originalSecret = "0123456789abcdef"
		)
		config, err := SealCreateConfig(box, guard, KindWebhook, CreateInput{
			URL: originalURL, SigningSecret: originalSecret,
		})
		if err != nil {
			t.Fatalf("SealCreateConfig: %v", err)
		}

		kept, err := MergeUpdateConfig(box, guard, KindWebhook, config, UpdateInput{Name: configTestPtr("Renamed")})
		if err != nil {
			t.Fatalf("MergeUpdateConfig keep: %v", err)
		}
		target, err := OpenTarget(box, KindWebhook, kept)
		if err != nil {
			t.Fatalf("OpenTarget keep: %v", err)
		}
		if target.URL != originalURL || target.SigningSecret != originalSecret {
			t.Fatalf("target after keep = %+v", target)
		}

		rotatedURL := "https://example.com/o2"
		rotatedSecret := "replacement-secret"
		rotated, err := MergeUpdateConfig(box, guard, KindWebhook, kept, UpdateInput{
			URL: configTestPtr(rotatedURL), SigningSecret: configTestPtr(rotatedSecret),
		})
		if err != nil {
			t.Fatalf("MergeUpdateConfig rotate: %v", err)
		}
		target, err = OpenTarget(box, KindWebhook, rotated)
		if err != nil {
			t.Fatalf("OpenTarget rotate: %v", err)
		}
		if target.URL != rotatedURL || target.SigningSecret != rotatedSecret {
			t.Fatalf("target after rotate = %+v", target)
		}
		view, err := ChannelView(sqlc.AlertChannel{Kind: KindWebhook, Config: rotated})
		if err != nil {
			t.Fatalf("ChannelView rotate: %v", err)
		}
		if view.URLPreview != "https://example.com/…" || !view.SigningSecretSet {
			t.Fatalf("view after rotate = %+v", view)
		}

		cleared, err := MergeUpdateConfig(box, guard, KindWebhook, rotated, UpdateInput{ClearSigningSecret: true})
		if err != nil {
			t.Fatalf("MergeUpdateConfig clear: %v", err)
		}
		target, err = OpenTarget(box, KindWebhook, cleared)
		if err != nil {
			t.Fatalf("OpenTarget clear: %v", err)
		}
		if target.SigningSecret != "" {
			t.Fatalf("SigningSecret = %q after clear", target.SigningSecret)
		}
		view, err = ChannelView(sqlc.AlertChannel{Kind: KindWebhook, Config: cleared})
		if err != nil {
			t.Fatalf("ChannelView clear: %v", err)
		}
		if view.SigningSecretSet {
			t.Fatal("SigningSecretSet = true after clear")
		}

		_, err = MergeUpdateConfig(box, guard, KindWebhook, cleared, UpdateInput{URL: configTestPtr("")})
		var validation *ValidationError
		if !errors.As(err, &validation) || validation.Fields["url"] == "" {
			t.Fatalf("empty URL merge error = %v, want url field", err)
		}
	})

	t.Run("telegram merge keeps and rotates", func(t *testing.T) {
		const (
			token  = "123456:ABC"
			chatID = "-100123"
		)
		config, err := SealCreateConfig(box, guard, KindTelegram, CreateInput{BotToken: token, ChatID: chatID})
		if err != nil {
			t.Fatalf("SealCreateConfig: %v", err)
		}

		newChat := "@ops_alerts"
		merged, err := MergeUpdateConfig(box, guard, KindTelegram, config, UpdateInput{ChatID: configTestPtr(newChat)})
		if err != nil {
			t.Fatalf("MergeUpdateConfig chat: %v", err)
		}
		target, err := OpenTarget(box, KindTelegram, merged)
		if err != nil {
			t.Fatalf("OpenTarget: %v", err)
		}
		if target.BotToken != token || target.ChatID != newChat {
			t.Fatalf("target = %+v", target)
		}

		newToken := "654321:XYZ"
		merged, err = MergeUpdateConfig(box, guard, KindTelegram, merged, UpdateInput{BotToken: configTestPtr(newToken)})
		if err != nil {
			t.Fatalf("MergeUpdateConfig token: %v", err)
		}
		target, err = OpenTarget(box, KindTelegram, merged)
		if err != nil {
			t.Fatalf("OpenTarget: %v", err)
		}
		if target.BotToken != newToken || target.ChatID != newChat {
			t.Fatalf("target = %+v", target)
		}

		_, err = MergeUpdateConfig(box, guard, KindTelegram, merged, UpdateInput{BotToken: configTestPtr("")})
		var validation *ValidationError
		if !errors.As(err, &validation) || validation.Fields["bot_token"] == "" {
			t.Fatalf("empty token merge error = %v, want bot_token field", err)
		}
		_, err = MergeUpdateConfig(box, guard, KindTelegram, merged, UpdateInput{ChatID: configTestPtr("")})
		if !errors.As(err, &validation) || validation.Fields["chat_id"] == "" {
			t.Fatalf("empty chat merge error = %v, want chat_id field", err)
		}
	})

	t.Run("corrupt and email configs", func(t *testing.T) {
		if _, err := MergeUpdateConfig(box, guard, KindWebhook, []byte(`{`), UpdateInput{}); !errors.Is(err, ErrInvalidChannelConfig) {
			t.Fatalf("corrupt merge error = %v, want ErrInvalidChannelConfig", err)
		}
		if _, err := MergeUpdateConfig(box, guard, KindEmail, nil, UpdateInput{}); !errors.Is(err, ErrEmailChannel) {
			t.Fatalf("email merge error = %v, want ErrEmailChannel", err)
		}
		if _, err := ChannelView(sqlc.AlertChannel{Kind: KindWebhook, Config: []byte(`{`)}); !errors.Is(err, ErrInvalidChannelConfig) {
			t.Fatalf("corrupt view error = %v, want ErrInvalidChannelConfig", err)
		}
		if _, err := OpenTarget(box, KindWebhook, []byte(`{`)); !errors.Is(err, ErrInvalidChannelConfig) {
			t.Fatalf("corrupt open error = %v, want ErrInvalidChannelConfig", err)
		}
		if _, err := OpenTarget(box, KindWebhook, []byte(`{"url_enc":""}`)); !errors.Is(err, ErrInvalidChannelConfig) {
			t.Fatalf("missing URL open error = %v, want ErrInvalidChannelConfig", err)
		}
		if _, err := OpenTarget(box, KindWebhook, []byte(`{"url_enc":"v1:not-a-ciphertext"}`)); !errors.Is(err, ErrInvalidChannelConfig) {
			t.Fatalf("bad ciphertext open error = %v, want ErrInvalidChannelConfig", err)
		}
		if _, err := OpenTarget(box, KindTelegram, []byte(`{"chat_id":"room one"}`)); !errors.Is(err, ErrInvalidChannelConfig) {
			t.Fatalf("corrupt chat id open error = %v, want ErrInvalidChannelConfig", err)
		}
		if _, err := OpenTarget(box, "carrier-pigeon", nil); !errors.Is(err, ErrInvalidChannelConfig) {
			t.Fatalf("unknown kind open error = %v, want ErrInvalidChannelConfig", err)
		}
	})

	t.Run("validate name", func(t *testing.T) {
		if _, err := ValidateName("  "); err == nil {
			t.Fatal("ValidateName(blank) succeeded")
		}
		name, err := ValidateName("  Deploy hook  ")
		if err != nil {
			t.Fatalf("ValidateName: %v", err)
		}
		if name != "Deploy hook" {
			t.Fatalf("name = %q", name)
		}
		if _, err := ValidateName(strings.Repeat("a", 61)); err == nil {
			t.Fatal("ValidateName(61 runes) succeeded")
		}
		if _, err := ValidateName(strings.Repeat("a", 60)); err != nil {
			t.Fatalf("ValidateName(60 runes): %v", err)
		}
	})
}
