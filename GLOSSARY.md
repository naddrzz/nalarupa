# Nalarupa

An AI design editor: a person and design agents build documents (pages, app screens, slide decks) together on one
canvas.

## Design agent

**Design agent**:
The AI that builds and edits designs in a document from a person's request.
_Avoid_: bot, assistant, AI designer

**Brief**:
What a person asks the design agent to make, with the purpose, audience and content it implies.
_Avoid_: prompt (when meaning the request's substance), spec

**Framing**:
The design agent's own opinionated angle on a brief, stated in a line, which the design direction then serves.
_Avoid_: summary, interpretation, brief restatement

**Design direction**:
The one deliberate visual idea a design follows: a concept stated in a sentence and the small discipline that
carries it (palette and type with a role for each, layout and spacing system, and one signature move).
_Avoid_: theme, style preset, archetype, vibe

**Signature move**:
The one deliberate, memorable element of a design direction that makes the design recognisable.
_Avoid_: hero element, wow factor, gimmick

**Fixation**:
Falling back on familiar defaults or seen examples instead of answering the brief.
_Avoid_: laziness, being generic (as a cause)

**Existing style**:
The visual language a document already has (its variables, components, and finished frames) that new work extends.
_Avoid_: brand (unless it is the client's actual brand), design system (unless one is defined)

**Critique pass**:
The design agent's review of a finished section against the critique checklist, followed by its fixes.
_Avoid_: self-review, QA, polish pass

**Critique checklist**:
The short fixed list of qualities a critique pass checks: hierarchy, alignment, spacing rhythm, contrast, content
realism, fixation, and distinctness from the generic default.
_Avoid_: rubric, design rules

**Generic default**:
The look a design falls into without a design direction: whatever layout, palette and type is reached for by habit,
such as a centred hero over identical cards on white, or a cream page with a large serif headline and a terracotta
accent.
_Avoid_: template look, AI look

**Sameness**:
Unrelated briefs coming back looking alike, the visible sign of fixation; an eval run flags it as a defect.
_Avoid_: consistency (that is a virtue within one design), repetition

**Variation axes**:
The four ways two design directions can differ: framing, layout structure, type pairing, and color logic.
_Avoid_: style dimensions, variety

**Variation**:
One of several alternative designs for the same brief, each following a different design direction.
_Avoid_: option, version, alternative (as a noun for the design)

## Evaluation

**Eval brief**:
A fixed brief kept for comparing the design agent's output before and after a change.
_Avoid_: test prompt, benchmark case

**Eval run**:
One pass of every eval brief through the design agent, with its screenshots and measurements kept for comparison.
_Avoid_: benchmark, test run
