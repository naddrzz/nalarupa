// Package scripttest holds script node runs for tests, so a test shows that something does not wait for a run by
// doing it while the run is held, instead of timing it. It takes the hook setter (script.SetRunHook) as an argument
// rather than importing package script, so script's own tests can use it too.
package scripttest

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// HoldRuns installs a run hook with set that holds every script node run whose source contains marker until
// release is called (or the test ends); started gets a value as each held run begins.
func HoldRuns(t testing.TB, set func(func(src string)), marker string) (started <-chan struct{}, release func()) {
	ch := make(chan struct{}, 64)
	gate := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	set(func(src string) {
		if strings.Contains(src, marker) {
			select {
			case ch <- struct{}{}:
			default:
			}
			<-gate
		}
	})
	t.Cleanup(func() {
		release()
		set(nil)
	})
	return ch, release
}

// Returns runs f and fails the test if f has not returned within a minute: with the runs it would wait for held,
// a wait shows up as a hang instead of a slow pass.
func Returns(t testing.TB, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	Within(t, done, what+" waited for a held script run")
}

// Started waits for a held run to begin.
func Started(t testing.TB, started <-chan struct{}, what string) {
	t.Helper()
	Within(t, started, what+": no run started")
}

// Within waits for ch to deliver (or close) and fails the test with msg after a minute.
func Within[T any](t testing.TB, ch <-chan T, msg string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(time.Minute):
		t.Fatal(msg)
	}
	var zero T
	return zero
}
