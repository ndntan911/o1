package incidents

import "errors"

// ErrInvalidChannelConfig is returned when an enabled alert channel has a
// malformed configuration. Callers should roll back the probe transaction.
var ErrInvalidChannelConfig = errors.New("invalid alert channel config")
