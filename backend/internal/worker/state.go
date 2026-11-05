// Package worker executes probe jobs and advances monitor state.
package worker

// maxConsecutive caps the failure/success counters so they never grow without
// bound.
const maxConsecutive = 1000

// State is the state-machine subset the worker mutates.
type State struct {
	Status    string
	Failures  int
	Successes int
}

// Advance applies one probe outcome to st and returns the new state. The
// status is one of "unknown", "up", or "down"; counters cap at 1000.
func Advance(st State, success bool, confirm, recover int) State {
	if success {
		st.Successes++
		if st.Successes > maxConsecutive {
			st.Successes = maxConsecutive
		}
		st.Failures = 0
		switch st.Status {
		case "unknown":
			st.Status = "up"
		case "down":
			if st.Successes >= recover {
				st.Status = "up"
			}
		}
		return st
	}

	st.Failures++
	if st.Failures > maxConsecutive {
		st.Failures = maxConsecutive
	}
	st.Successes = 0
	switch st.Status {
	case "unknown", "up":
		if st.Failures >= confirm {
			st.Status = "down"
		}
	}
	return st
}
