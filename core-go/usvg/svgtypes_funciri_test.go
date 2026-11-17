// Port of svgtypes 0.16.1 src/funciri.rs tests

package usvg

import "testing"

func TestSTIRI(t *testing.T) {
	for _, c := range []struct{ text, want string }{{"#id", "id"}, {"   #id   ", "id"}, {"#1", "1"}} {
		got, err := stParseIRI(c.text)
		if err != nil || string(got) != c.want {
			t.Errorf("stParseIRI(%q) = %q, %v", c.text, got, err)
		}
	}
	// Trailing data is ok for the stream, but not for IRI.
	s := stNewStream("   #id   text")
	if got, err := s.parseIRI(); err != nil || got != "id" {
		t.Errorf("parseIRI = %q, %v", got, err)
	}
	if _, err := stParseIRI("   #id   text"); err == nil || err.Error() != "unexpected data at position 10" {
		t.Errorf("stParseIRI trailing error = %v", err)
	}
	if _, err := stParseIRI("# id"); err == nil || err.Error() != "invalid value" {
		t.Errorf("stParseIRI(# id) error = %v", err)
	}
}

func TestSTFuncIRI(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"url(#id)", "id"}, {"url(#1)", "1"}, {"    url(    #id    )   ", "id"},
		{"url('#id')", "id"}, {"url(' #id ')", "id"}, {"url(\"#id\")", "id"}, {"url(\" #id \")", "id"},
	} {
		got, err := stParseFuncIRI(c.text)
		if err != nil || string(got) != c.want {
			t.Errorf("stParseFuncIRI(%q) = %q, %v", c.text, got, err)
		}
	}
	s := stNewStream("url(#id) qwe")
	if got, err := s.parseFuncIRI(); err != nil || got != "id" {
		t.Errorf("parseFuncIRI = %q, %v", got, err)
	}
	for _, c := range []struct{ text, want string }{
		{"url(#id) qwe", "unexpected data at position 10"},
		{"url ( #1 )", "expected 'url(' not 'url ' at position 1"},
		{"url(#)", "invalid value"},
		{"url(# id)", "invalid value"},
		// Quotes around the ID must be on both sides.
		{"url('#id)", "unexpected end of stream"},
		{"url(#id')", "invalid value"},
	} {
		_, err := stParseFuncIRI(c.text)
		if err == nil || err.Error() != c.want {
			t.Errorf("stParseFuncIRI(%q) error = %v; want %q", c.text, err, c.want)
		}
	}
}
