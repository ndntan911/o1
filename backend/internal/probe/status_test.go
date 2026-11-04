package probe

import (
	"strings"
	"testing"
)

func TestExpectedStatus(t *testing.T) {
	cases := []struct {
		spec string
		code int
		ok   bool
	}{
		{"2xx", 204, true},
		{"2xx", 301, false},
		{"200,204", 204, true},
		{"200-299", 250, true},
		{"200-299", 300, false},
	}
	for _, tc := range cases {
		st, err := ParseExpectedStatus(tc.spec)
		if err != nil {
			t.Fatalf("ParseExpectedStatus(%q): %v", tc.spec, err)
		}
		if got := st.Matches(tc.code); got != tc.ok {
			t.Errorf("%q matches %d = %v, want %v", tc.spec, tc.code, got, tc.ok)
		}
	}
	for _, bad := range []string{"", "abc", "99", "600", "200-", "200,", "2xx,,3xx"} {
		if _, err := ParseExpectedStatus(bad); err == nil {
			t.Errorf("ParseExpectedStatus(%q) succeeded", bad)
		}
	}
}

func TestExpectedStatus_MoreCases(t *testing.T) {
	cases := []struct {
		spec string
		code int
		ok   bool
	}{
		{"2XX", 200, true},
		{"1xx", 100, true},
		{"5xx", 599, true},
		{"200-299,301", 301, true},
		{"200-299,301", 302, false},
		{" 204 ", 204, true},
		{"200, 204", 204, true},
	}
	for _, tc := range cases {
		st, err := ParseExpectedStatus(tc.spec)
		if err != nil {
			t.Fatalf("ParseExpectedStatus(%q): %v", tc.spec, err)
		}
		if got := st.Matches(tc.code); got != tc.ok {
			t.Errorf("%q matches %d = %v, want %v", tc.spec, tc.code, got, tc.ok)
		}
	}
	for _, bad := range []string{
		"2x", "2xxx", "200-99", "600-699", "299-200", "2xx-300", "20x",
		"200,204,", ",200", "200--201", "200-299-300",
	} {
		if _, err := ParseExpectedStatus(bad); err == nil {
			t.Errorf("ParseExpectedStatus(%q) succeeded", bad)
		}
	}
	many := strings.TrimSuffix(strings.Repeat("200,", 21), ",")
	if _, err := ParseExpectedStatus(many); err == nil {
		t.Error("ParseExpectedStatus with 21 entries succeeded")
	}
}

func TestExpectedStatus_String(t *testing.T) {
	cases := map[string]string{
		"2xx":         "200-299",
		"200,204":     "200,204",
		"200-299,301": "200-299,301",
		"1xx,3xx":     "100-199,300-399",
	}
	for in, want := range cases {
		st, err := ParseExpectedStatus(in)
		if err != nil {
			t.Fatalf("ParseExpectedStatus(%q): %v", in, err)
		}
		if got := st.String(); got != want {
			t.Errorf("ParseExpectedStatus(%q).String() = %q, want %q", in, got, want)
		}
	}
	var zero ExpectedStatus
	if zero.Matches(200) {
		t.Error("zero ExpectedStatus matched 200")
	}
}
