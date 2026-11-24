package copilot

import "o1/internal/config"

// LimitsFromConfig maps the copilot settings onto the runner's caps. Every
// field falls back to DefaultLimits when the config value is zero or negative,
// so a hand-built Config (for example in a test) keeps the production limits
// instead of silently disabling one. The per-incident and per-workspace start
// caps live on Service because only the API enqueues investigations.
func LimitsFromConfig(cfg config.Config) Limits {
	limits := DefaultLimits()
	if cfg.CopilotMaxModelCalls > 0 {
		limits.MaxModelCalls = cfg.CopilotMaxModelCalls
	}
	if cfg.CopilotMaxToolCalls > 0 {
		limits.MaxToolCalls = cfg.CopilotMaxToolCalls
	}
	if cfg.CopilotMaxResultBytes > 0 {
		limits.MaxResultBytes = cfg.CopilotMaxResultBytes
	}
	if cfg.CopilotMaxOutputTokens > 0 {
		limits.MaxOutputTokens = cfg.CopilotMaxOutputTokens
	}
	if cfg.CopilotMaxInputTokens > 0 {
		limits.MaxInputTokens = cfg.CopilotMaxInputTokens
	}
	if cfg.CopilotTimeout > 0 {
		limits.Timeout = cfg.CopilotTimeout
	}
	if cfg.CopilotCallTimeout > 0 {
		limits.CallTimeout = cfg.CopilotCallTimeout
	}
	return limits
}
