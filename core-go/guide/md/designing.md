# Designing

## Steps

1. **Read the brief.** Who it is for, what they must do or feel, which content
   is fixed, what the document already holds.
2. **Frame it.** In one line, say what this piece is from your own angle
   ("a departure board, not a brochure").
3. **Choose a design direction.** Name two or three, each grown from a
   different side of the subject and apart on the **variation axes**: framing,
   layout structure, type pairing, color logic. Pick the one that best serves
   the framing; a designer handed a direction by the lead builds that one.
   With an existing style (variables, components, finished screens), extend
   it; a new direction is for a blank canvas, a requested new look, variations.
4. **Post it** before you build, so the user can redirect you: as your first
   chat message, before any tool call but the look below. Your prompt's editor
   state holds the existing style (variables, fonts, components); only without
   it, look first (`get_editor_state`, or `GetVariables` and `Get`), then post:
   `Framing:` the line from step 2
   `Direction:` concept · palette · type · signature move
   `Why:` one line on why it beats the other directions
5. **Lock tokens & shared shell, then build.** Define semantic `SetVariables`,
   build reusable chrome/surface motifs (`reusable: true`) for multi-screen
   flows, then build one `execute` per section top-down via statement-level
   `Insert` calls (use `fill_container` on rows and multi-column cards).
6. **Critique each finished section** on its screenshot against the critique
   checklist and fix the worst failure first; at most two revision calls per
   section, then move on. Fix any execute-report warning right away.

## A design direction

- **Concept:** one sentence drawn from the subject (its setting, materials,
  era, rituals, data, voice), so unrelated briefs come back looking unrelated.
- **Palette:** 3–5 colors, each with one job (ground, surface, text, accent…),
  stored as variables. Keep identical ground and surface textures across
  sibling screens; never drift from textured paper on page 1 to flat on page 2.
- **Type:** a display face and a text face (or one family), each with one job,
  on a scale with clear steps.
- **Layout and spacing:** a grid chosen for this content (a timetable, a
  ledger, a poster, an index) and one spacing scale used everywhere.
- **Signature move:** the one memorable element that carries the concept (an
  oversized numeral, a full-bleed rule, a stamp), repeated across screens.

## Critique checklist

- **Hierarchy:** one dominant element per view, made with scale contrast and
  white space before boxes or color.
- **Alignment:** every edge on the grid; zero right-edge clipping or overflow.
- **Spacing rhythm:** scale steps only; tight within groups, wide between them.
- **Contrast:** each color judged against its neighbors; body text at least
  4.5:1, large text 3:1.
- **Content:** specific copy from the brief: names, dates, prices, numbers,
  quotes; lorem and generic claims are placeholders, not copy.
- **Fixation:** every familiar choice is there because the framing calls for
  it, not by habit.
- **Distinctness:** away from the generic default, carrying the signature move.

More in `read_guide` topics `design` (scale, mobile, dashboards), `slides`.
