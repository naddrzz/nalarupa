package jsonv

import (
	"strings"
	"testing"
)

func TestRoundTripKeepsOrderAndNumberText(t *testing.T) {
	src := `{"z":1,"a":[10.0,-0.5,1e2,{}],"m":{"b":null,"a":true},"e":[],"s":"tab\t\u0001   </> é \"q\" \\"}`
	v, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(Marshal(v)); got != `{"z":1,"a":[10.0,-0.5,1e2,{}],"m":{"b":null,"a":true},"e":[],"s":"tab\t\u0001 `+" "+` </> é \"q\" \\"}` {
		t.Fatalf("compact: %s", got)
	}
	want := "{\n  \"z\": 1,\n  \"a\": [\n    10.0,\n    -0.5,\n    1e2,\n    {}\n  ],\n  \"m\": {\n    \"b\": null,\n    \"a\": true\n  },\n  \"e\": [],\n  \"s\": \"tab\\t\\u0001   </> é \\\"q\\\" \\\\\"\n}"
	if got := string(MarshalPretty(v)); got != want {
		t.Fatalf("pretty:\n%s\nwant:\n%s", got, want)
	}
}

func TestRepeatedKeyKeepsFirstPosition(t *testing.T) {
	v := MustParse(`{"a":1,"b":2,"a":3}`)
	if got := string(Marshal(v)); got != `{"a":3,"b":2}` {
		t.Fatal(got)
	}
}

func TestRemoveSwapsInTheLastKey(t *testing.T) {
	o := MustParse(`{"a":1,"b":2,"c":3,"d":4}`).(*Object)
	o.Remove("b")
	if got := strings.Join(o.Keys(), ","); got != "a,d,c" {
		t.Fatal(got)
	}
	if v, _ := o.Get("d"); v != Number("4") {
		t.Fatal(v)
	}
	o.Set("b", Number("5"))
	if got := string(Marshal(o)); got != `{"a":1,"d":4,"c":3,"b":5}` {
		t.Fatal(got)
	}
}

func TestParseErrors(t *testing.T) {
	for _, src := range []string{`{"a": [1, 2`, "{\n  \"a\": 1,\n", `{"a" 1}`, `[1,]`, `{"a":1} x`, `[1] [2]`, ``, `nul`} {
		_, err := Parse([]byte(src))
		if err == nil || !strings.Contains(err.Error(), " at line ") {
			t.Errorf("%q: %v", src, err)
		}
	}
	_, err := Parse([]byte("{\n  \"a\": 1,\n"))
	if !strings.Contains(err.Error(), "line 3") {
		t.Error(err)
	}
	// serde_json's recursion limit: 127 levels parse, 128 do not.
	if _, err := Parse([]byte(strings.Repeat("[", 127) + strings.Repeat("]", 127))); err != nil {
		t.Error(err)
	}
	if _, err := Parse([]byte(strings.Repeat("[", 128) + strings.Repeat("]", 128))); err == nil {
		t.Error("128 levels parsed")
	}
}

func TestFormatFloatLikeSerde(t *testing.T) {
	for f, want := range map[float64]string{
		0: "0.0", 10: "10.0", -90: "-90.0", 0.1: "0.1", 1.5: "1.5", 1234.5678: "1234.5678",
		1e15: "1000000000000000.0", 1e16: "1e16", 1.5e16: "1.5e16", 1e-5: "0.00001", 1.5e-5: "0.000015",
		1e-6: "1e-6", 1.25e-7: "1.25e-7", 123456789012345680: "1.2345678901234568e17", 0.3: "0.3",
	} {
		if got := FormatFloat(f); got != want {
			t.Errorf("%v: %s, want %s", f, got, want)
		}
	}
}

func TestNumbersLikeSerde(t *testing.T) {
	if Equal(Number("10"), Number("10.0")) || !Equal(Number("10.0"), Number("1e1")) || !Equal(Number("10"), Int(10)) {
		t.Error("== compares by kind")
	}
	if !SameNumber("10", "10.0") || !SameNumber("-3", "-3.0") || SameNumber("10", "10.5") || SameNumber("9007199254740993", "9007199254740992.0") {
		t.Error("same_number")
	}
	if i, ok := Number("-9223372036854775808").Int64(); !ok || i != -9223372036854775808 {
		t.Error(i, ok)
	}
	if Number("-0").IsFloat() != true || Number("18446744073709551616").IsFloat() != true || Number("5").IsFloat() {
		t.Error("kinds")
	}
	if !Equal(MustParse(`{"a":1,"b":[2]}`), MustParse(`{"b":[2],"a":1}`)) {
		t.Error("objects compare without key order")
	}
}
