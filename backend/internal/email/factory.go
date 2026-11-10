package email

import (
	"fmt"
	"time"

	"o1/internal/config"
)

const (
	// senderAttempts is the total number of deliveries per message.
	senderAttempts = 3
	// senderBackoff is the first retry wait; the next one grows fourfold.
	senderBackoff = 250 * time.Millisecond
)

// NewSender builds the transactional email sender configured by cfg. Every
// provider is wrapped in a RetrySender that retries transient failures up to
// three times. Invalid or incomplete configuration returns an error that never
// echoes credentials.
func NewSender(cfg config.Config) (Sender, error) {
	provider, err := cfg.ResolveEmailProvider()
	if err != nil {
		return nil, err
	}

	var inner Sender
	switch provider {
	case config.EmailProviderMailpit:
		inner = NewMailpitSender(cfg.SMTPAddr, cfg.EmailFrom)
	case config.EmailProviderResend:
		inner = NewResendSender(
			cfg.ResendAPIKey,
			cfg.EmailFrom,
			cfg.EmailReplyTo,
			cfg.ResendAPIURL,
			cfg.ResendRPS,
			cfg.ResendBurst,
		)
	default:
		// ResolveEmailProvider only returns the two providers handled above.
		return nil, fmt.Errorf("email: unsupported provider %q", provider)
	}

	return NewRetrySender(inner, senderAttempts, senderBackoff), nil
}
