package e2e

import (
	"strings"
	"testing"
)

// M0 exit criterion: one trivial E2E test drives the empty binary.

func TestEmptyBatch(t *testing.T) {
	res := RunDM(t, t.TempDir(), "", nil)
	if res.Code != 0 {
		t.Fatalf("exit %d, stderr: %s", res.Code, res.Stderr)
	}
	if res.Stdout != "◾0 ok\n" {
		t.Fatalf("stdout %q, want %q", res.Stdout, "◾0 ok\n")
	}
}

func TestUnknownSubcommand(t *testing.T) {
	res := RunDM(t, t.TempDir(), "", nil, "frobnicate")
	if res.Code != 2 {
		t.Fatalf("exit %d, want 2 (stderr: %s)", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "unknown subcommand") {
		t.Fatalf("stderr %q, want unknown-subcommand error", res.Stderr)
	}
}
