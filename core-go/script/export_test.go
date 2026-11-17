package script

// Hooks for the external tests.

// EnablePanicNative adds `__PANIC`, a native that panics, to every later execute sandbox.
func EnablePanicNative() { panicNative = true }

// TimeLimitText is the error of a snippet stopped at budget.
var TimeLimitText = timeLimitText
