package probe

import (
	"fmt"
	"strconv"
	"strings"
)

const maxExpectedStatusEntries = 20

type statusSpan struct {
	start int
	end   int
}

// ExpectedStatus is a parsed expected-status specification. The zero value
// matches no codes.
type ExpectedStatus struct {
	spans []statusSpan
}

// ParseExpectedStatus parses a comma-separated list of classes ("2xx"),
// exact codes ("204"), and inclusive ranges ("200-299"). Codes must be
// between 100 and 599 and a specification may contain at most 20 entries.
func ParseExpectedStatus(spec string) (ExpectedStatus, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return ExpectedStatus{}, fmt.Errorf("probe: expected status is empty")
	}
	parts := strings.Split(spec, ",")
	if len(parts) > maxExpectedStatusEntries {
		return ExpectedStatus{}, fmt.Errorf("probe: expected status has more than %d entries", maxExpectedStatusEntries)
	}
	status := ExpectedStatus{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return ExpectedStatus{}, fmt.Errorf("probe: expected status has an empty entry")
		}
		span, err := parseStatusSpan(part)
		if err != nil {
			return ExpectedStatus{}, err
		}
		duplicate := false
		for _, existing := range status.spans {
			if existing == span {
				duplicate = true
				break
			}
		}
		if !duplicate {
			status.spans = append(status.spans, span)
		}
	}
	return status, nil
}

// Matches reports whether code falls in any configured span.
func (s ExpectedStatus) Matches(code int) bool {
	for _, span := range s.spans {
		if code >= span.start && code <= span.end {
			return true
		}
	}
	return false
}

// String returns the normalized specification, e.g. "200-299,301".
func (s ExpectedStatus) String() string {
	parts := make([]string, 0, len(s.spans))
	for _, span := range s.spans {
		if span.start == span.end {
			parts = append(parts, strconv.Itoa(span.start))
			continue
		}
		parts = append(parts, strconv.Itoa(span.start)+"-"+strconv.Itoa(span.end))
	}
	return strings.Join(parts, ",")
}

func parseStatusSpan(part string) (statusSpan, error) {
	if dash := strings.IndexByte(part, '-'); dash >= 0 {
		if strings.Count(part, "-") != 1 {
			return statusSpan{}, fmt.Errorf("probe: invalid expected status range")
		}
		start, err := parseStatusCode(part[:dash])
		if err != nil {
			return statusSpan{}, err
		}
		end, err := parseStatusCode(part[dash+1:])
		if err != nil {
			return statusSpan{}, err
		}
		if start > end {
			return statusSpan{}, fmt.Errorf("probe: expected status range is inverted")
		}
		return statusSpan{start: start, end: end}, nil
	}

	if len(part) == 3 &&
		(part[1] == 'x' || part[1] == 'X') &&
		(part[2] == 'x' || part[2] == 'X') {
		class := part[0]
		if class < '1' || class > '5' {
			return statusSpan{}, fmt.Errorf("probe: invalid expected status class")
		}
		start := int(class-'0') * 100
		return statusSpan{start: start, end: start + 99}, nil
	}

	code, err := parseStatusCode(part)
	if err != nil {
		return statusSpan{}, err
	}
	return statusSpan{start: code, end: code}, nil
}

func parseStatusCode(raw string) (int, error) {
	if len(raw) != 3 {
		return 0, fmt.Errorf("probe: expected status code must be three digits")
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return 0, fmt.Errorf("probe: expected status code must be three digits")
		}
	}
	code, err := strconv.Atoi(raw)
	if err != nil || code < 100 || code > 599 {
		return 0, fmt.Errorf("probe: expected status code must be between 100 and 599")
	}
	return code, nil
}
