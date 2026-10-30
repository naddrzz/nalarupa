//! Agent-facing documentation (`read_guide`) and style archetypes (`get_style`).

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

struct Style {
    name: &'static str,
    mood: &'static str,
    fonts: (&'static str, &'static str),
    palette: &'static [(&'static str, &'static str)],
    radius: &'static str,
    elevation: &'static str,
    layout: &'static str,
}

const STYLES: &[Style] = &[
    Style {
        name: "Editorial Serif",
        mood: "Magazine calm: large serif headlines, generous whitespace, thin rules, photography-led.",
        fonts: ("Playfair Display", "Inter"),
        palette: &[("background", "#F7F4EE"), ("surface", "#FFFFFF"), ("text", "#1B1A17"), ("muted", "#6B665E"), ("border", "#E3DDD2"), ("accent", "#B5462B")],
        radius: "0–4px; mostly square edges",
        elevation: "none; separate with 1px rules (#E3DDD2)",
        layout: "Asymmetric 12-column feel: wide text column + narrow caption column. Headlines 64–120px, tight leading 1.05.",
    },
    Style {
        name: "Swiss Grid",
        mood: "International style: strict grid, bold grotesk type, one loud accent, flush-left everything.",
        fonts: ("Inter Tight", "Inter"),
        palette: &[("background", "#FFFFFF"), ("surface", "#F2F2F2"), ("text", "#0A0A0A"), ("muted", "#6E6E6E"), ("border", "#0A0A0A"), ("accent", "#E3262B")],
        radius: "0px",
        elevation: "none",
        layout: "Hard columns of equal width (fill_container) with 24px gutters. Oversized numbers and labels in caps with +2 letterSpacing.",
    },
    Style {
        name: "Soft Bento",
        mood: "Friendly product UI: rounded tiles of different sizes, pastel surfaces, playful icons.",
        fonts: ("Plus Jakarta Sans", "Plus Jakarta Sans"),
        palette: &[("background", "#F4F2FA"), ("surface", "#FFFFFF"), ("text", "#1E1B2E"), ("muted", "#6F6A85"), ("tile-a", "#E7E0FF"), ("tile-b", "#DFF5EA"), ("tile-c", "#FFE9D6"), ("accent", "#6D4AFF")],
        radius: "20–28px tiles, 12px controls, pills for tags",
        elevation: "shadow {offset:{x:0,y:8}, blur:24, color:#1E1B2E14}",
        layout: "Bento grid built from rows of tiles with mixed widths (one 2×, two 1×). 16–20px gaps.",
    },
    Style {
        name: "Midnight Signal",
        mood: "Dark developer tool: near-black surfaces, neon accent, monospace details, crisp borders.",
        fonts: ("Geist", "Geist Mono"),
        palette: &[("background", "#0B0C0F"), ("surface", "#14161B"), ("raised", "#1C1F26"), ("text", "#EDEEF0"), ("muted", "#8B90A0"), ("border", "#262A33"), ("accent", "#3DF2A3"), ("accent-2", "#7C8CFF")],
        radius: "8–12px",
        elevation: "1px borders instead of shadows; glow on accent: shadow {blur:24, color:#3DF2A333}",
        layout: "Dense, aligned panels. Code snippets and metrics in mono. Gradient-free except a subtle radial glow behind the hero.",
    },
    Style {
        name: "Warm Minimal",
        mood: "Quiet lifestyle brand: warm neutrals, soft sans, lots of air, earthy accent.",
        fonts: ("DM Sans", "DM Sans"),
        palette: &[("background", "#FBF8F3"), ("surface", "#F1EBE1"), ("text", "#2B2621"), ("muted", "#857B70"), ("border", "#E6DED2"), ("accent", "#C0703F")],
        radius: "12–16px images, pill buttons",
        elevation: "very soft: shadow {offset:{x:0,y:2}, blur:12, color:#2B262110}",
        layout: "Centered single column for heroes, then alternating image/text rows. Body 17–18px, line height 1.6.",
    },
    Style {
        name: "Brutal Mono",
        mood: "Raw and confident: monospace everything, thick black borders, saturated blocks, no polish.",
        fonts: ("Space Mono", "Space Mono"),
        palette: &[("background", "#FFFDF5"), ("block-1", "#FFE14D"), ("block-2", "#FF8FB1"), ("block-3", "#7DF9FF"), ("text", "#000000"), ("border", "#000000")],
        radius: "0px",
        elevation: "hard offset shadow {offset:{x:6,y:6}, blur:0, color:#000000}",
        layout: "Blocky stacked sections with 3px strokes, uppercase labels, oversized buttons.",
    },
];

pub fn style(name: &str) -> ToolOut {
    let name = name.trim();
    if name.is_empty() {
        let list: String = STYLES.iter().map(|s| format!("- **{}** — {}\n", s.name, s.mood)).collect();
        return ToolOut::ok(format!("# Styles\n\nCall get_style with a name to load one.\n\n{list}"));
    }
    let Some(s) = STYLES.iter().find(|s| s.name.eq_ignore_ascii_case(name)) else {
        return ToolOut::err(format!("unknown style '{name}'; call get_style with no name to list them"));
    };
    let tokens: String = s.palette.iter().map(|(k, v)| format!("  \"{k}\": {{type: \"color\", value: \"{v}\"}},\n")).collect();
    ToolOut::ok(format!(
        "# {}\n\n{}\n\n## Typography\n- Display: {}\n- Text: {}\n\n## Shape\n- Radius: {}\n- Elevation: {}\n\n## Layout\n{}\n\n## Tokens (pass to SetVariables)\n```js\nSetVariables({{\n{tokens}  \"font-display\": {{type: \"string\", value: \"{}\"}},\n  \"font-text\": {{type: \"string\", value: \"{}\"}}\n}})\n```\n",
        s.name, s.mood, s.fonts.0, s.fonts.1, s.radius, s.elevation, s.layout, s.fonts.0, s.fonts.1
    ))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn fonts_in(text: &str) -> Vec<String> {
        text.split("fontFamily: \"").skip(1).filter_map(|r| r.split('"').next()).map(str::to_string).collect()
    }

    #[test]
    fn worked_examples_do_not_all_teach_one_font() {
        let fonts: Vec<String> = TOPICS.iter().flat_map(|(_, body)| fonts_in(body)).collect();
        let first = fonts.first().expect("the guide has examples with fonts");
        assert!(fonts.iter().any(|f| f != first), "every example uses {first}");
    }

    #[test]
    fn the_design_topic_offers_type_pairings_and_slides_vary_layouts() {
        let design = read("design").text;
        let pairings = design.lines().filter(|l| l.starts_with("- ") && l.contains(" + ")).count();
        assert!(pairings >= 3, "at least three display + text pairings: {design}");
        assert!(design.contains("one option, not the default"), "{design}");
        let slides = read("slides").text;
        assert!(!slides.contains("reuse its structure"), "slides vary their layouts: {slides}");
        assert!(slides.contains("one design direction"), "{slides}");
    }
}
