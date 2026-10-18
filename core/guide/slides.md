# Slides

A deck is simply the document's top-level frames, in document order. Each
top-level frame is one slide. Present mode (toolbar ▶ or Ctrl+Enter) steps
through them with the arrow keys; the Slides tab lists them; Export → PNG/JPEG/
WebP/SVG writes one file per slide, and `Export([], "pdf", "deck.pdf")` (or the
Slides tab's PDF button) writes the whole deck as one multi-page PDF.

## Format

- 1920×1080 (16:9) unless the user asks otherwise. `clip: true` on every slide.
- Lay slides out left to right on one row with a 100px gap (`x = i * 2020`)
  so the canvas reads like the deck.
- Name slides `01 Cover`, `02 Problem`… The name is what the Slides tab shows.
- Define the deck's tokens once with `SetVariables` (background, foreground,
  muted, accent, display font, body font) and bind every slide to them.

## Writing

- One idea per slide. If it doesn't fit, split it — never shrink the type.
- Lead with the claim as the title ("Churn fell 32% after onboarding v2"),
  not a topic label ("Churn").
- Body text ≥ 28px, titles ≥ 56px, cover/statement titles 96–160px.
- At most two font families. Keep ≥ 120px from the slide edges.
- 2–3 colors plus neutrals. Use the accent for the one thing to notice.
- Open with a strong cover and close with a single memorable statement or
  call to action.

## Layout patterns

| pattern | structure |
| --- | --- |
| Cover | vertical, `justifyContent: "end"`, kicker + huge title + subtitle |
| Statement | one centered sentence, nothing else |
| Split | horizontal: text column (`fill_container`) + visual block |
| Three points | title + horizontal row of 3 `fill_container` columns |
| Big number | giant figure + one line of context |
| Timeline | horizontal row of step frames joined by thin rectangles |
| Chart | title + bar chart built from frames (`alignItems: "end"`) |
| Quote | large quote text + attribution |
| Closing | statement or CTA + contact line |

Build the first slide fully, screenshot it, then reuse its structure with
`Copy` for the rest of the deck so spacing stays consistent.
