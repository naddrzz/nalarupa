//! Agent-facing documentation (`read_guide`) and worked examples of design directions (`get_style`).

use crate::engine::ToolOut;

const TOPICS: &[(&str, &str)] = &[
    ("schema", include_str!("../guide/schema.md")),
    ("execute", include_str!("../guide/execute.md")),
    ("components", include_str!("../guide/components.md")),
    ("slides", include_str!("../guide/slides.md")),
    ("design", include_str!("../guide/design.md")),
    ("code", include_str!("../guide/code.md")),
    ("designing", DESIGNING),
];

/// The design core: the process and the design direction. Every built-in agent always gets it (`agent::instructions`).
pub const DESIGNING: &str = include_str!("../guide/designing.md");

pub const OVERVIEW: &str = include_str!("../guide/overview.md");

pub fn read(topic: &str) -> ToolOut {
    let t = topic.trim().trim_end_matches(".md").to_ascii_lowercase();
    if t.is_empty() || t == "overview" {
        return ToolOut::ok(OVERVIEW);
    }
    match TOPICS.iter().find(|(name, _)| *name == t) {
        Some((_, body)) => ToolOut::ok(*body),
        None => ToolOut::err(format!("unknown topic '{topic}'; topics: {}", TOPICS.iter().map(|(n, _)| *n).collect::<Vec<_>>().join(", "))),
    }
}

/// Core instructions every API agent session gets (overview + design core + schema + execute).
pub fn system_docs() -> String {
    format!("{OVERVIEW}\n\n{DESIGNING}\n\n{}\n\n{}", TOPICS[0].1, TOPICS[1].1)
}

/// A worked example of a design direction: how one grows from a brief. Agents learn the reasoning; they do not paste it.
struct Direction {
    name: &'static str,
    brief: &'static str,
    framing: &'static str,
    concept: &'static str,
    palette: &'static [(&'static str, &'static str, &'static str)],
    type_: (&'static str, &'static str),
    layout: &'static str,
    signature: &'static str,
    why: &'static str,
}

const DIRECTIONS: &[Direction] = &[
    Direction {
        name: "Tide Table",
        brief: "Site for a harbour town's ferry, with sailings that move with the tide.",
        framing: "a tide table you can read on the quay, not a tourism page",
        concept: "the printed tide almanac every harbour office pins up",
        palette: &[("ground", "chart white", "#F3F6F1"), ("text", "deep-water ink", "#0F2A3D"), ("next sailing", "buoy red", "#D2382B"), ("secondary", "mudflat grey", "#7D857B")],
        type_: ("Barlow Condensed for times and headings", "Source Serif 4 for notes and prose"),
        layout: "12 columns with a fixed 160px time column on the left; every row is a sailing; 8px spacing scale, 48px between days.",
        signature: "the day's tide curve drawn as one thin line running behind the timetable rows",
        why: "the people using it are standing on the quay checking a time; the almanac is the form they already trust.",
    },
    Direction {
        name: "Seed Packet",
        brief: "Page for a community seed library that lends seeds and asks for some back after harvest.",
        framing: "a seed packet, not a shop catalogue",
        concept: "the hand-stamped packets on the library's shelf",
        palette: &[("ground", "allotment green", "#2F4A2C"), ("text", "chalk", "#EDEBDD"), ("accent", "seed-head yellow", "#F2C230"), ("secondary", "sage", "#9DB08A")],
        type_: ("Bricolage Grotesque for packet titles", "Work Sans for instructions"),
        layout: "packet-shaped cards of uneven heights in three columns, each a variety; 4/8 spacing, generous 64px section gaps.",
        signature: "a rotated 'sow by' date stamp opening every section",
        why: "lending and returning seeds is the whole idea, and the packet carries both the variety and the promise to give some back.",
    },
    Direction {
        name: "Running Statement",
        brief: "Landing page for invoicing software for sole traders.",
        framing: "a statement you can trust, not a fintech pitch",
        concept: "the monthly statement the product produces, shown as the page itself",
        palette: &[("ground", "paper", "#FFFFFF"), ("text", "ink", "#1A1A1A"), ("money in", "ledger green", "#1E6B4A"), ("rules", "rule grey", "#D6D6D6")],
        type_: ("IBM Plex Mono for every figure and date", "IBM Plex Sans for prose"),
        layout: "a tabular grid with figures right-aligned on a decimal column; 4px rhythm inside tables, 96px between sections.",
        signature: "a running balance column that carries on down the whole page, section after section",
        why: "the audience fears losing track of money; a calm, exact ledger shows the product's character before any claim does.",
    },
    Direction {
        name: "Wall Label",
        brief: "Exhibition page for a small museum's show of one photographer.",
        framing: "the label beside the work, not a poster for it",
        concept: "the museum's own wall labels and hanging plan",
        palette: &[("ground", "gallery wall", "#E9E6E1"), ("text", "black", "#111111"), ("accent", "ultramarine", "#2438C8"), ("secondary", "graphite", "#6B6B6B")],
        type_: ("Instrument Serif for titles", "Public Sans for labels and prose"),
        layout: "photographs large and off-centre, captions in a narrow side column; 8px scale, wide margins.",
        signature: "a label block (title, year, medium) pinned beside every photograph",
        why: "the photographs are the content; the page should behave like the room they hang in.",
    },
    Direction {
        name: "Pit Wall",
        brief: "Live dashboard for a sim-racing league's race night.",
        framing: "the pit wall screen, not a sports news site",
        concept: "the timing screens race engineers read at a glance",
        palette: &[("ground", "carbon", "#121417"), ("text", "white", "#F5F5F5"), ("personal best", "timing green", "#3DDC84"), ("fastest overall", "sector purple", "#A855F7"), ("slower", "amber", "#F5B700")],
        type_: ("Chakra Petch for positions and lap times", "Barlow for labels"),
        layout: "a dense 24-column grid, one row per driver, columns per sector; 4px spacing inside rows.",
        signature: "sector bars coloured green, purple and amber as the only visual system",
        why: "viewers want to see who is fast right now; the colour code racing already uses does that without a legend.",
    },
    Direction {
        name: "Picture Book",
        brief: "Reading app screen for children aged five to seven.",
        framing: "a picture book you can tap, not a learning platform",
        concept: "the spreads of a much-loved picture book",
        palette: &[("ground", "sky", "#BFE3F5"), ("text", "night ink", "#23305A"), ("highlight", "sunshine", "#FFD23F"), ("accent", "tomato", "#F25C3B")],
        type_: ("Baloo 2 for titles", "Nunito at 22px or more for reading"),
        layout: "one big illustration per screen with the text below at reading size; 24px rounded corners, 16/24 spacing.",
        signature: "a hand-drawn underline that follows the word being read aloud",
        why: "a child reads one sentence at a time; the page should do one thing and show where they are.",
    },
];

/// `get_style`: the worked examples of design directions (no name lists them).
pub fn directions(name: &str) -> ToolOut {
    let name = name.trim();
    if name.is_empty() {
        let list: String = DIRECTIONS.iter().map(|s| format!("- **{}**: {}\n", s.name, s.brief)).collect();
        return ToolOut::ok(format!(
            "# Design directions: worked examples\n\nEach shows how a design direction grows from one brief. Read one for the reasoning, then compose \
             your own from your brief; it is a worked example, not a paste target. Call get_style with a name to read one.\n\n{list}"
        ));
    }
    let Some(s) = DIRECTIONS.iter().find(|s| s.name.eq_ignore_ascii_case(name)) else {
        return ToolOut::err(format!("unknown example '{name}'; call get_style with no name to list them"));
    };
    let palette: String = s.palette.iter().map(|(role, what, hex)| format!("  - {role}: {what} {hex}\n")).collect();
    ToolOut::ok(format!(
        "# {}\n\n- **Brief:** {}\n- **Framing:** {}\n- **Concept:** {}\n- **Palette** (one job each):\n{palette}\
         - **Type:** display {}; text {}\n- **Layout and spacing:** {}\n- **Signature move:** {}\n- **Why:** {}\n\n\
         Your brief is not this one: take the reasoning (subject → framing → direction), not the values.\n",
        s.name, s.brief, s.framing, s.concept, s.type_.0, s.type_.1, s.layout, s.signature, s.why
    ))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn styles_are_worked_examples_of_design_directions() {
        let list = directions("");
        assert!(!list.error && list.text.contains("worked example") && list.text.contains("not a paste target"), "{}", list.text);
        assert_eq!(list.text.matches("\n- **").count(), DIRECTIONS.len(), "every example is listed");
        for s in DIRECTIONS {
            let one = directions(s.name);
            assert!(!one.error, "{}", s.name);
            for part in ["Brief", "Framing", "Concept", "Palette", "Type", "Layout and spacing", "Signature move", "Why"] {
                assert!(one.text.contains(&format!("**{part}")), "{} shows its {part}", s.name);
            }
            assert!(!one.text.contains("SetVariables"), "{} offers no tokens to paste", s.name);
        }
        assert!(directions("no-such-style").error, "an unknown name is still rejected");
    }
}
