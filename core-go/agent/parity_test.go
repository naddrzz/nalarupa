package agent

// Parity with the Rust core: `nalarupa-cli agent-instructions <backend>` prints what a main agent is told, and
// Instructions must match it byte for byte for every backend (a provider gets the API backend's). Live only: the
// text holds the guide files as checked out (line endings included), so a committed golden would differ between
// checkouts; without NALARUPA_CLI the test is skipped, and the tests above check its parts.

import (
	"os"
	"os/exec"
	"testing"
)

func TestInstructionsMatchRust(t *testing.T) {
	cli := os.Getenv("NALARUPA_CLI")
	if cli == "" {
		t.Skip("NALARUPA_CLI is not set")
	}
	// The test cache never rechecks files outside this module, so a rebuilt CLI would get a cached pass. A file in
	// the module written during the run is too new to cache: live runs always rerun.
	if err := os.WriteFile(".parity-live", nil, 0o666); err != nil {
		t.Fatal(err)
	}
	for _, backend := range []string{"api", "claude-code"} {
		out, err := exec.Command(cli, "agent-instructions", backend).Output()
		if err != nil {
			t.Fatal(backend, err)
		}
		got, want := Instructions(backend), string(out)
		if got == want {
			continue
		}
		i := 0
		for i < len(got) && i < len(want) && got[i] == want[i] {
			i++
		}
		t.Errorf("%s: %d bytes, Rust %d; first difference at byte %d: %q vs %q", backend, len(got), len(want), i,
			got[i:min(i+60, len(got))], want[i:min(i+60, len(want))])
	}
}
