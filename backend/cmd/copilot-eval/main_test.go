package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun_ProviderOffExitsTwo(t *testing.T) {
	t.Setenv("O1_AI_PROVIDER", "")
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("run returned %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "O1_AI_PROVIDER") {
		t.Fatalf("stderr = %q, want a message naming O1_AI_PROVIDER", stderr.String())
	}
}

func TestRun_StubWritesReport(t *testing.T) {
	t.Setenv("O1_AI_PROVIDER", "stub")
	out := filepath.Join(t.TempDir(), "copilot-eval.md")
	var stdout, stderr bytes.Buffer

	code := run([]string{
		"-fixtures", filepath.Join("..", "..", "internal", "copilot", "testdata", "eval"),
		"-out", out,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run returned %d, want 0; stderr = %s", code, stderr.String())
	}

	raw, err := os.ReadFile(out) //nolint:gosec // test temp dir path
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	report := string(raw)
	for _, want := range []string{
		"# Copilot evaluation",
		"Stub-only run",
		"not evidence of model quality",
		"| timeout |",
		"**Pass rate:",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report is missing %q", want)
		}
	}
}
