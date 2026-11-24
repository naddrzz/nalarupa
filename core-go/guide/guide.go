// Package guide is the agent-facing documentation (`read_guide`) and the worked examples of design directions
// (`get_style`). The topics live in md/*.md and are embedded at build time.
package guide

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed md/*.md
var files embed.FS

func topic(name string) string {
	b, err := files.ReadFile("md/" + name + ".md")
	if err != nil {
		panic(err) // embedded at build time
	}
	return string(b)
}

// topics are read_guide's topics in their listed order.
var topics = []struct{ name, body string }{
	{"schema", topic("schema")},
	{"execute", topic("execute")},
	{"components", topic("components")},
	{"slides", topic("slides")},
	{"design", topic("design")},
	{"code", topic("code")},
	{"designing", Designing},
}

// Designing is the design core: the process and the design direction. Every built-in agent always gets it.
var Designing = topic("designing")

// Overview is what read_guide without a topic returns.
var Overview = topic("overview")

// asciiLower is Rust's `to_ascii_lowercase`.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// Read is `read_guide`: a topic's text, or the error text and true for an unknown one.
func Read(name string) (string, bool) {
	t := strings.TrimSpace(name)
	for strings.HasSuffix(t, ".md") {
		t = strings.TrimSuffix(t, ".md")
	}
	t = asciiLower(t)
	if t == "" || t == "overview" {
		return Overview, false
	}
	names := make([]string, len(topics))
	for i, tp := range topics {
		if tp.name == t {
			return tp.body, false
		}
		names[i] = tp.name
	}
	return fmt.Sprintf("unknown topic '%s'; topics: %s", name, strings.Join(names, ", ")), true
}

// SystemDocs is the core instructions every API agent session gets (overview, design core, schema, execute).
func SystemDocs() string {
	return Overview + "\n\n" + Designing + "\n\n" + topics[0].body + "\n\n" + topics[1].body
}

// direction is a worked example of a design direction: how one grows from a brief.
type direction struct {
	name, brief, framing, concept string
	palette                       [][3]string // role, what, hex
	display, text                 string
	layout, signature, why        string
}

var directions = []direction{
	{
		name:      "Tide Table",
		brief:     "Site for a harbour town's ferry, with sailings that move with the tide.",
		framing:   "a tide table you can read on the quay, not a tourism page",
		concept:   "the printed tide almanac every harbour office pins up",
		palette:   [][3]string{{"ground", "chart white", "#F3F6F1"}, {"text", "deep-water ink", "#0F2A3D"}, {"next sailing", "buoy red", "#D2382B"}, {"secondary", "mudflat grey", "#7D857B"}},
		display:   "Barlow Condensed for times and headings",
		text:      "Source Serif 4 for notes and prose",
		layout:    "12 columns with a fixed 160px time column on the left; every row is a sailing; 8px spacing scale, 48px between days.",
		signature: "the day's tide curve drawn as one thin line running behind the timetable rows",
		why:       "the people using it are standing on the quay checking a time; the almanac is the form they already trust.",
	},
	{
		name:      "Seed Packet",
		brief:     "Page for a community seed library that lends seeds and asks for some back after harvest.",
		framing:   "a seed packet, not a shop catalogue",
		concept:   "the hand-stamped packets on the library's shelf",
		palette:   [][3]string{{"ground", "allotment green", "#2F4A2C"}, {"text", "chalk", "#EDEBDD"}, {"accent", "seed-head yellow", "#F2C230"}, {"secondary", "sage", "#9DB08A"}},
		display:   "Bricolage Grotesque for packet titles",
		text:      "Work Sans for instructions",
		layout:    "packet-shaped cards of uneven heights in three columns, each a variety; 4/8 spacing, generous 64px section gaps.",
		signature: "a rotated 'sow by' date stamp opening every section",
		why:       "lending and returning seeds is the whole idea, and the packet carries both the variety and the promise to give some back.",
	},
	{
		name:      "Running Statement",
		brief:     "Landing page for invoicing software for sole traders.",
		framing:   "a statement you can trust, not a fintech pitch",
		concept:   "the monthly statement the product produces, shown as the page itself",
		palette:   [][3]string{{"ground", "paper", "#FFFFFF"}, {"text", "ink", "#1A1A1A"}, {"money in", "ledger green", "#1E6B4A"}, {"rules", "rule grey", "#D6D6D6"}},
		display:   "IBM Plex Mono for every figure and date",
		text:      "IBM Plex Sans for prose",
		layout:    "a tabular grid with figures right-aligned on a decimal column; 4px rhythm inside tables, 96px between sections.",
		signature: "a running balance column that carries on down the whole page, section after section",
		why:       "the audience fears losing track of money; a calm, exact ledger shows the product's character before any claim does.",
	},
	{
		name:      "Wall Label",
		brief:     "Exhibition page for a small museum's show of one photographer.",
		framing:   "the label beside the work, not a poster for it",
		concept:   "the museum's own wall labels and hanging plan",
		palette:   [][3]string{{"ground", "gallery wall", "#E9E6E1"}, {"text", "black", "#111111"}, {"accent", "ultramarine", "#2438C8"}, {"secondary", "graphite", "#6B6B6B"}},
		display:   "Instrument Serif for titles",
		text:      "Public Sans for labels and prose",
		layout:    "photographs large and off-centre, captions in a narrow side column; 8px scale, wide margins.",
		signature: "a label block (title, year, medium) pinned beside every photograph",
		why:       "the photographs are the content; the page should behave like the room they hang in.",
	},
	{
		name:      "Pit Wall",
		brief:     "Live dashboard for a sim-racing league's race night.",
		framing:   "the pit wall screen, not a sports news site",
		concept:   "the timing screens race engineers read at a glance",
		palette:   [][3]string{{"ground", "carbon", "#121417"}, {"text", "white", "#F5F5F5"}, {"personal best", "timing green", "#3DDC84"}, {"fastest overall", "sector purple", "#A855F7"}, {"slower", "amber", "#F5B700"}},
		display:   "Chakra Petch for positions and lap times",
		text:      "Barlow for labels",
		layout:    "a dense 24-column grid, one row per driver, columns per sector; 4px spacing inside rows.",
		signature: "sector bars coloured green, purple and amber as the only visual system",
		why:       "viewers want to see who is fast right now; the colour code racing already uses does that without a legend.",
	},
	{
		name:      "Picture Book",
		brief:     "Reading app screen for children aged five to seven.",
		framing:   "a picture book you can tap, not a learning platform",
		concept:   "the spreads of a much-loved picture book",
		palette:   [][3]string{{"ground", "sky", "#BFE3F5"}, {"text", "night ink", "#23305A"}, {"highlight", "sunshine", "#FFD23F"}, {"accent", "tomato", "#F25C3B"}},
		display:   "Baloo 2 for titles",
		text:      "Nunito at 22px or more for reading",
		layout:    "one big illustration per screen with the text below at reading size; 24px rounded corners, 16/24 spacing.",
		signature: "a hand-drawn underline that follows the word being read aloud",
		why:       "a child reads one sentence at a time; the page should do one thing and show where they are.",
	},
}

// Directions is `get_style`: the list of worked examples (no name) or one of them, or the error text and true
// for an unknown name.
func Directions(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		var list strings.Builder
		for _, s := range directions {
			fmt.Fprintf(&list, "- **%s**: %s\n", s.name, s.brief)
		}
		return "# Design directions: worked examples\n\nEach shows how a design direction grows from one brief. Read one for the reasoning, then compose " +
			"your own from your brief; it is a worked example, not a paste target. Call get_style with a name to read one.\n\n" + list.String(), false
	}
	for _, s := range directions {
		if !asciiFold(s.name, name) {
			continue
		}
		var palette strings.Builder
		for _, p := range s.palette {
			fmt.Fprintf(&palette, "  - %s: %s %s\n", p[0], p[1], p[2])
		}
		return fmt.Sprintf("# %s\n\n- **Brief:** %s\n- **Framing:** %s\n- **Concept:** %s\n- **Palette** (one job each):\n%s"+
			"- **Type:** display %s; text %s\n- **Layout and spacing:** %s\n- **Signature move:** %s\n- **Why:** %s\n\n"+
			"Your brief is not this one: take the reasoning (subject → framing → direction), not the values.\n",
			s.name, s.brief, s.framing, s.concept, palette.String(), s.display, s.text, s.layout, s.signature, s.why), false
	}
	return fmt.Sprintf("unknown example '%s'; call get_style with no name to list them", name), true
}

// asciiFold is Rust's `eq_ignore_ascii_case`.
func asciiFold(a, b string) bool { return asciiLower(a) == asciiLower(b) }
