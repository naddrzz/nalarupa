package script

import (
	"strings"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

func obj(t *testing.T, s string) *jsonv.Object {
	t.Helper()
	o := jsonv.Obj(jsonv.MustParse(s))
	if o == nil {
		t.Fatalf("%s is not an object", s)
	}
	return o
}

func TestScriptHeaderAndInputs(t *testing.T) {
	schema, has, decl, err := scriptHeader("/**\n * @schema 2.10\n * @input n: number(min=1, max=5) = 3\n * @input mode: enum(\"a\", \"b\") = \"b\"\n * @input on: boolean = true\n * @input c: color = #ff0000\n * @input label: string = \"Hi \\\"x\\\"\"\n * @input target: ref\n */")
	if err != nil || !has || schema != "2.10" {
		t.Fatalf("schema %q %v %v", schema, has, err)
	}
	var names []string
	for _, i := range decl {
		names = append(names, i.name)
	}
	if strings.Join(names, ",") != "n,mode,on,c,label,target" {
		t.Fatalf("names %v", names)
	}
	if decl[4].def != "Hi \"x\"" {
		t.Fatalf("label default %#v", decl[4].def)
	}
	v, st := scriptInputs(decl, obj(t, `{"n": 9, "mode": "a", "extra": 1}`))
	if st != nil {
		t.Fatal(st.Title)
	}
	if got, want := string(jsonv.Marshal(v)), `{"n":5.0,"mode":"a","on":true,"c":"#ff0000","label":"Hi \"x\"","target":""}`; got != want {
		t.Fatalf("inputs %s, want %s", got, want)
	}
	if _, st := scriptInputs(decl, obj(t, `{"mode": "z"}`)); st == nil || st.Title != "Invalid input value" {
		t.Fatalf("bad enum: %+v", st)
	}
	for _, c := range []struct{ src, msg string }{
		{"@input x: vec3", "Invalid @input type 'vec3'"},
		{"@input x: number(step=2)", "unknown arg 'step'"},
		{"@input x: enum(\"a\") = \"q\"", "is not one of the options"},
		{"@input x: string = 4", "must be a quoted string"},
	} {
		_, _, _, err := scriptHeader("/** " + c.src + " */")
		if err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s: %v", c.src, err)
		}
	}
}

// Every directive error, word for word as script.rs words it.
func TestHeaderErrors(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		{"@input", "@input must start with a name"},
		{"@input 3: number", "@input must start with a name"},
		{"@input x number", "@input 'x': expected ':'"},
		{"@input x:", "@input 'x' is missing a type"},
		{"@input x: number(min=)", "@input 'x' arg 'min' must be a literal, got ')'"},
		{"@input x: enum(,)", "@input 'x' arg must be a literal, got ','"},
		{"@input x: enum(\"a\" \"b\")", "@input 'x' arg list: expected ')', got 'b'"},
		{"@input x: enum(\"a\"", "@input 'x' arg list: expected ')', got end of input"},
		{"@input x: number = 1 2", "@input 'x' has unexpected trailing token '2'"},
		{"@input x: string(1)", "@input 'x' of type 'string' does not accept arguments"},
		{"@input x: number(1)", "@input number 'x' does not accept positional arguments"},
		{"@input x: number(min=\"a\")", "@input number 'x' arg 'min' must be a number, got 'a'"},
		{"@input x: number = \"a\"", "@input 'x' default must be a number, got 'a'"},
		{"@input x: number =", "@input 'x' default must be a number, got end of input"},
		{"@input x: boolean = 1", "@input 'x' default must be 'true' or 'false', got '1'"},
		{"@input x: color = 1", "@input 'x' color default must be a hex literal like #RRGGBB, got '1'"},
		{"@input x: ref = 1", "@input 'x' ref default must be an identifier or quoted string, got '1'"},
		{"@input x: enum(a=\"b\")", "@input enum 'x' does not accept named arguments"},
		{"@input x: enum()", "@input enum 'x' must specify at least one option"},
		{"@input x: enum(1)", "@input enum 'x' option must be a quoted string, got '1'"},
		{"@input x: enum(\"a\") = b", "@input 'x' enum default must be a quoted string, got 'b'"},
		{"@input x: enum(\"\") = true", "@input 'x' enum default must be a quoted string, got 'true'"},
		{"@input x: string = \"open", "Unterminated string"},
		{"@input x: color = #", "Expected hex digits after '#'"},
		{"@input x: number = -", "Invalid number '-'"},
		{"@input x: number = .", "Invalid number '.'"},
		{"@input x: number = 1;", "Unexpected character ';'"},
		{"@input x: string = é", "Unexpected character 'é'"},
		{"@input x: enum(\"\", 2)", "@input enum 'x' option must be a quoted string, got '2'"},
	} {
		_, _, _, err := scriptHeader("/** " + c.src + " */")
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: got %v, want %q", c.src, err, c.want)
		}
	}
	// An empty string shows as its kind.
	if got := (tok{kind: tokStr}).show(); got != "string" {
		t.Errorf("empty string shows as %q", got)
	}
}

func TestHeaderBlocks(t *testing.T) {
	// Directives come from every /** */ block; plain comments, unknown directives and text before an @ are
	// ignored; a repeated input replaces the earlier one and moves to the end; the last @schema wins.
	src := "// @schema 1.0\n/* @input skipped: number */\n/** @schema 2.0\n * @unknown x\n * text @input no: number\n * @input a: number = 1\n * @input b: string */\nconst x = 1;\n/**\r\n * @input a: boolean\r\n * @schema  2.19 \r\n */\n/** unclosed @input c: number"
	schema, has, decl, err := scriptHeader(src)
	if err != nil || !has || schema != "2.19" {
		t.Fatalf("schema %q %v %v", schema, has, err)
	}
	if len(decl) != 2 || decl[0].name != "b" || decl[1].name != "a" || decl[1].kind != kindBoolean {
		t.Fatalf("decl %+v", decl)
	}
	if _, has, _, _ := scriptHeader("return [];"); has {
		t.Fatal("no header has no schema")
	}
	if schema, has, _, _ := scriptHeader("/** @schema */"); !has || schema != "" {
		t.Fatalf("an empty @schema is present and empty: %q %v", schema, has)
	}
}

// lines is Rust's str::lines: only a `\r` before a `\n` goes.
func TestLines(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"\n", []string{""}},
		{"a", []string{"a"}},
		{"a\n", []string{"a"}},
		{"a\r\nb\r\n", []string{"a", "b"}},
		{"a\n\nb", []string{"a", "", "b"}},
		{"a\r", []string{"a\r"}},
		{"a\r\nb\r", []string{"a", "b\r"}},
		{"a\r\r\n", []string{"a\r"}},
	} {
		got := Lines(c.in)
		if len(got) != len(c.want) || strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%q: got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestInputCoercion(t *testing.T) {
	_, _, decl, err := scriptHeader(`/**
 * @input n: number(min=1, max=5) = 3
 * @input free: number = 2.5
 * @input low: number(min=-2)
 * @input s: string = "d"
 * @input b: boolean
 * @input c: color
 * @input r: ref = node1
 * @input e: enum("x", "y")
 */`)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ given, want string }{
		// Missing and null take the defaults.
		{`{}`, `{"n":3.0,"free":2.5,"low":0.0,"s":"d","b":false,"c":"#000000","r":"node1","e":"x"}`},
		{`{"n": null, "e": null}`, `{"n":3.0,"free":2.5,"low":0.0,"s":"d","b":false,"c":"#000000","r":"node1","e":"x"}`},
		// Numbers clamp; numeric strings parse (trimmed); anything else is the minimum, or 0.
		{`{"n": -4, "free": "1e400", "low": -7}`, `{"n":1.0,"free":null,"low":-2.0,"s":"d","b":false,"c":"#000000","r":"node1","e":"x"}`},
		{`{"n": " 4.5 ", "free": "x", "low": true}`, `{"n":4.5,"free":0.0,"low":-2.0,"s":"d","b":false,"c":"#000000","r":"node1","e":"x"}`},
		{`{"n": "NaN", "free": "inf", "low": "0x10"}`, `{"n":1.0,"free":null,"low":-2.0,"s":"d","b":false,"c":"#000000","r":"node1","e":"x"}`},
		// Other kinds keep the stored value as it is, whatever its type.
		{`{"s": 4, "b": "yes", "c": {"a": 1}, "r": [1], "e": "y"}`, `{"n":3.0,"free":2.5,"low":0.0,"s":4,"b":"yes","c":{"a":1},"r":[1],"e":"y"}`},
	} {
		v, st := scriptInputs(decl, obj(t, c.given))
		if st != nil {
			t.Errorf("%s: %s", c.given, st.Title)
			continue
		}
		if got := string(jsonv.Marshal(v)); got != c.want {
			t.Errorf("%s: got %s, want %s", c.given, got, c.want)
		}
	}
	for _, c := range []struct{ given, want string }{
		{`{"e": "z"}`, "Value 'z' is not allowed for input 'e'. Must be one of: x, y"},
		{`{"e": 3}`, "Value '3' is not allowed for input 'e'. Must be one of: x, y"},
		{`{"e": {"k": "x"}}`, `Value '{"k":"x"}' is not allowed for input 'e'. Must be one of: x, y`},
	} {
		_, st := scriptInputs(decl, obj(t, c.given))
		if st == nil || st.Title != "Invalid input value" || *st.Description != c.want {
			t.Errorf("%s: %+v", c.given, st)
		}
	}
}
