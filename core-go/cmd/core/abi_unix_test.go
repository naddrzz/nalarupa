//go:build !windows

package main

import (
	"strings"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/cmd/core/cthread"
)

// The app's JavaScript threads call in, and the JavaScript engine suspends them with a signal to scan their
// stacks: Go code run on such a thread left it with Go's alternate signal stack, the engine found the handler on
// that stack, backed off and retried forever (chat-check hung on Linux). A call from a C thread answers and
// leaves the thread's signal stack as it was.
func TestACallFromAForeignThreadLeavesItsSignalStack(t *testing.T) {
	for range 3 {
		out, same := cthread.Call(`{"cmd":"state"}`)
		if !strings.HasPrefix(out, `{"ok":true`) {
			t.Fatalf("answer %q", out)
		}
		if !same {
			t.Fatal("the call changed the calling thread's alternate signal stack")
		}
	}
}
