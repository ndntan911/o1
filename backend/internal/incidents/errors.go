package incidents

import "errors"

// ErrInvalidChannelConfig identifies malformed alert channel configuration.
// The probe lifecycle skips malformed channels instead of failing the probe
// transaction; API validation can use this sentinel when rejecting config.
var ErrInvalidChannelConfig = errors.New("invalid alert channel config")
