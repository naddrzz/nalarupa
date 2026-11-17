package qjs

import "testing"

// A native's panic is raised again by every call that can run JavaScript: getters and toString too.
func TestNativePanicReachesEveryEntryPoint(t *testing.T) {
	r, err := New(64<<20, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.Define("boom", func([]string) (string, error) { panic("boom") }); err != nil {
		t.Fatal(err)
	}
	r.Free(r.Eval(`globalThis.o = {get x() { return boom() }, toString() { return boom() }};
Object.defineProperty(globalThis, "g", {get() { return boom() }})`, "t.js", false))
	o := r.Global("o")
	defer r.Free(o)
	for name, f := range map[string]func(){
		"Get":      func() { r.Free(r.Get(o, "x")) },
		"Global":   func() { r.Free(r.Global("g")) },
		"ToString": func() { r.ToString(o) },
	} {
		func() {
			defer func() {
				if p := recover(); p != "boom" {
					t.Errorf("%s: recovered %v", name, p)
				}
			}()
			f()
		}()
	}
}
