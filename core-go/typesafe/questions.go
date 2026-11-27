// Package typesafe integrates TypeSafe AI System One models (such as Jev) for
// visual per-screen UI design evaluation.
//
// Following TypeSafe integration principles:
//   - Only the rendered visual image of a single finished/edited screen is sent in `state` (no code or JSON AST).
//   - All atomic judgments (Score and Noul primitives) live in one module and run in parallel in a single request.
//   - Jev supplies calibrated perceptual judgments; Go code computes the 0–100 composite score and pass/revise truth table.
package typesafe

import (
	"fmt"
	"math"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

const (
	// DefaultBaseURL is the official TypeSafe AI API root.
	DefaultBaseURL = "https://api.typesafe.ai"
	// DefaultModel is the alias pointing to the newest stable Jev release (currently jev-1.14.0).
	DefaultModel = "jev-latest"
	// DefaultPassScore is the default 1–100 composite score threshold below which auto-revision triggers.
	DefaultPassScore = 75
	// DefaultMaxRevisions is the default maximum auto-revision rounds per screen in a single turn.
	DefaultMaxRevisions = 2

	// Standard Noul thresholds recommended by TypeSafe System One guidance:
	// >= 0.70 reads as yes (defect present), <= 0.30 reads as no (clean), and (0.30, 0.70) is borderline/uncertain.
	NoulYes = 0.70
	NoulNo  = 0.30
)

// KnownModels lists the primary TypeSafe vision-capable System One model IDs/aliases for UI selection.
var KnownModels = []string{
	"jev-latest",
	"jev-1.14.0",
}

// DimensionScore holds a 0–100 normalized sub-score for one perceptual UI criterion.
type DimensionScore struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Score  int    `json:"score"`  // 0..100 (higher is always better)
	Status string `json:"status"` // "good" | "warn" | "fail"
}

// Usage tracks TypeSafe input/output token counts for one evaluation request.
type Usage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// Decision is the deterministic code-computed outcome from Jev's typed perceptual judgments on one screen image.
type Decision struct {
	Model      string           `json:"model"`
	Score      int              `json:"score"`     // 0..100 composite score
	PassScore  int              `json:"passScore"` // configured 0..100 threshold
	Verdict    string           `json:"verdict"`   // "pass" | "revise"
	Dimensions []DimensionScore `json:"dimensions"`
	Blockers   []string         `json:"blockers,omitempty"`
	Warnings   []string         `json:"warnings,omitempty"`
	Usage      Usage            `json:"usage"`
}

// ClampPassScore clamps a configured revision score threshold to [1, 100] (defaulting <= 0 to DefaultPassScore).
func ClampPassScore(v int) int {
	if v <= 0 {
		return DefaultPassScore
	}
	if v < 1 {
		return 1
	}
	if v > 100 {
		return 100
	}
	return v
}

// ClampMaxRevisions clamps a configured max auto-revision count to [1, 5] (defaulting <= 0 to DefaultMaxRevisions).
func ClampMaxRevisions(v int) int {
	if v <= 0 {
		return DefaultMaxRevisions
	}
	if v < 1 {
		return 1
	}
	if v > 5 {
		return 5
	}
	return v
}

type scoreDimensionSpec struct {
	key          string
	label        string
	weight       float64
	instructions string
	levels       []string
	failFix      string
	warnFix      string
	criticalMin  float64 // normalized [0,1] floor below which the screen must revise even if weighted total >= PassScore
}

type noulDefectSpec struct {
	key          string
	label        string
	weight       float64
	instructions string
	trueDesc     string
	falseDesc    string
	penalty      float64 // subtracted from 0..100 composite when noul >= NoulYes
	failFix      string
	warnFix      string
}

// All atomic perceptual questions live together here so criteria and weights are auditable in one place.
// Every question refers solely to `screen` in `state` (the rendered PNG of a single screen).
var scoreSpecs = []scoreDimensionSpec{
	{
		key:          "visual_hierarchy",
		label:        "Hierarchy",
		weight:       0.20,
		instructions: "How clear is the visual hierarchy and primary focal point in `screen`?",
		levels: []string{
			"Elements compete at the same visual weight with no clear focal point, headline anchor, or reading order.",
			"A main heading or section exists, but secondary cards, labels, or buttons compete closely with it for attention.",
			"Clear primary focal area or headline with distinct secondary and tertiary levels and an obvious primary action.",
			"Immediate, effortless visual hierarchy: one dominant focal anchor, strong scale contrast, and an unmistakable reading path.",
		},
		failFix:     "Visual hierarchy is flat or competing: establish one dominant focal point using stronger headline/value scale contrast, quieter secondary metadata, and a single clear primary CTA.",
		warnFix:     "Visual hierarchy could be sharper: increase size/weight contrast between primary headings, key metrics, and secondary supporting labels.",
		criticalMin: 0.45,
	},
	{
		key:          "accessibility_contrast",
		label:        "Accessibility",
		weight:       0.18,
		instructions: "How legible and accessible are the text contrast and interactive control sizes in `screen`?",
		levels: []string{
			"Important text, labels, or buttons are faint, washed out, or nearly unreadable against their background.",
			"Main headings are readable, but body copy, muted captions, badges, or secondary controls have low contrast or cramped tiny text.",
			"All headings, body text, and interactive controls have clear, comfortable foreground-to-background contrast and readable sizes.",
			"Crisp, high-legibility contrast across every surface, card, input, and badge with generous, accessible click or tap targets.",
		},
		failFix:     "Accessibility & contrast failure: darken or lighten low-contrast text/captions/badges so body text achieves strong readability (>= 4.5:1) and ensure interactive controls are comfortably sized.",
		warnFix:     "Some secondary text, badges, or subtle controls look low-contrast: boost text/icon contrast against their background surface.",
		criticalMin: 0.45,
	},
	{
		key:          "color_harmony",
		label:        "Color harmony",
		weight:       0.10,
		instructions: "How cohesive and intentional is the color palette across backgrounds, surfaces, text, and accents in `screen`?",
		levels: []string{
			"Random, muddy, or disconnected colors with no consistent background, surface, and accent roles.",
			"Mostly usable colors, but too many competing bright accents or mismatched warm and cool grays weaken palette cohesion.",
			"Cohesive palette with consistent neutral surfaces and a clear, restrained accent color.",
			"Refined, harmonious color system where ground, elevated surfaces, text hierarchy, and a purposeful accent color work seamlessly together.",
		},
		failFix:     "Color palette lacks harmony: unify ground and card surface neutrals into one temperature family and restrict saturated accent color to primary actions/highlights (60-30-10 balance).",
		warnFix:     "Color roles are slightly busy: reduce competing accent colors so neutral surfaces carry the layout and one intentional accent guides focus.",
		criticalMin: 0.35,
	},
	{
		key:          "spacing_alignment",
		label:        "Spacing & grid",
		weight:       0.06,
		instructions: "How consistent and well-proportioned are the spacing, padding, and grid alignment in `screen`?",
		levels: []string{
			"Chaotic spacing, visibly misaligned edges, or content crammed tightly against container borders.",
			"Uneven gaps, awkward empty holes, or inconsistent padding and alignment across sibling cards or rows.",
			"Clean grid alignment and consistent container padding, with related items grouped closer together than separate sections.",
			"Disciplined spatial rhythm, crisp shared-axis alignment, and balanced breathing room inside and between every group.",
		},
		failFix: "Spacing or alignment looks uneven: align sibling containers to shared grid edges, give cards generous internal padding, and keep tighter gaps inside groups than between sections.",
		warnFix: "Tighten spatial rhythm by equalizing sibling paddings and aligning left/right container edges cleanly.",
	},
	{
		key:          "typography_craft",
		label:        "Typography",
		weight:       0.06,
		instructions: "How well-crafted are the typography scale, line lengths, and copy presentation in `screen`?",
		levels: []string{
			"Awkward font sizing, hard-to-scan walls of centered paragraph text, or obvious raw placeholder boilerplate.",
			"Acceptable fonts, but weak size contrast between labels and values, overly wide text lines, or generic filler copy.",
			"Clear typographic pairing and scale separating display headings, body copy, and compact metadata with realistic copy.",
			"Editorial-grade type hierarchy, comfortable reading measure, crisp numeric-vs-label contrast, and specific, authentic copy.",
		},
		failFix: "Typography & copy craft is weak: avoid wide centered paragraphs, establish clear display/body/caption steps, make data values bolder than their labels, and replace generic filler copy.",
		warnFix: "Refine typographic scale and line measure so headings, values, and supporting copy scan effortlessly.",
	},
}

var noulSpecs = []noulDefectSpec{
	{
		key:          "has_overlap_or_clipping",
		label:        "No overlap",
		weight:       0.18,
		instructions: "Do any text labels, icons, cards, or UI controls unintentionally overlap, collide, or get cut off at a container edge in `screen`?",
		trueDesc:     "Text runs into other text or icons, UI elements unintentionally stack or collide on top of each other, or content is visibly clipped or cut off at an edge.",
		falseDesc:    "Every text element, icon, card, and control sits cleanly in its own space with zero unintended collision, stacking, or cut-off content.",
		penalty:      12.0,
		failFix:      "Overlapping or clipped UI elements detected: separate colliding text/icons/cards, use `fill_container` / `fit_content` instead of cramped fixed boxes, and fix any clipped edges.",
		warnFix:      "Possible tight collision or near-clipping detected: check crowded rows, badges, and container heights so no text or icon touches or clips.",
	},
	{
		key:          "has_color_clash",
		label:        "No color clash",
		weight:       0.08,
		instructions: "Are there clashing, harsh, or visually vibrating color combinations in `screen`?",
		trueDesc:     "Harshly clashing hues, oversaturated vibrating colors placed directly against each other, or jarring text-on-background color collisions.",
		falseDesc:    "Adjacent and layered colors sit comfortably together without harsh chromatic vibration or jarring color collisions.",
		penalty:      10.0,
		failFix:      "Clashing colors detected: remove harsh saturated-on-saturated color collisions, tone down vibrating neon fills, and pair accents against calm neutral surfaces.",
		warnFix:      "Some adjacent color pairings feel slightly harsh: soften saturation or separate strong hues with neutral surface space.",
	},
	{
		key:          "is_ai_slop",
		label:        "Anti-AI slop",
		weight:       0.14,
		instructions: "Does `screen` look like generic, cookie-cutter AI-generated UI slop rather than an intentionally art-directed product or page?",
		trueDesc:     "Uses cliché default AI tropes such as neon purple-blue glow gradients, a generic centered slogan over three identical icon cards, excessive box borders around everything, or soulless boilerplate filler.",
		falseDesc:    "Shows a specific, intentional visual direction with tailored layout structure, restrained surface craft, whitespace grouping instead of heavy borders, and domain-authentic details.",
		penalty:      10.0,
		failFix:      "Design reads as generic AI slop: replace cliché purple/neon glows, cookie-cutter 3-identical-card rows, or excessive box borders with an intentional layout grid, cohesive surface hierarchy, and specific domain content.",
		warnFix:      "Parts of the screen lean toward generic template tropes: sharpen the signature visual motif, vary card/section composition, and rely on whitespace over repetitive borders.",
	},
}

// Questions builds the JSON `questions` object sent to `POST /v1/systemone`.
func Questions() *jsonv.Object {
	qs := jsonv.NewObject()
	for _, s := range scoreSpecs {
		levels := make([]jsonv.Value, len(s.levels))
		for i, lv := range s.levels {
			levels[i] = lv
		}
		q := jsonv.NewObject()
		q.Set("type", "score")
		q.Set("instructions", s.instructions)
		q.Set("criteria", jsonv.NewArray(levels...))
		qs.Set(s.key, q)
	}
	for _, n := range noulSpecs {
		crit := jsonv.NewObject()
		crit.Set("true", n.trueDesc)
		crit.Set("false", n.falseDesc)
		q := jsonv.NewObject()
		q.Set("type", "noul")
		q.Set("instructions", n.instructions)
		q.Set("criteria", crit)
		qs.Set(n.key, q)
	}
	return qs
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// ComputeDecision maps a raw SystemOneResponse JSON object into a calibrated 0–100 Decision using our truth table.
func ComputeDecision(resp jsonv.Value, passScore int) (*Decision, error) {
	passScore = ClampPassScore(passScore)
	answers := jsonv.Obj(jsonv.Get(resp, "answers"))
	if answers == nil {
		return nil, fmt.Errorf("TypeSafe response missing `answers` object")
	}

	modelUsed := jsonv.StrOr(jsonv.Get(resp, "model"))
	if modelUsed == "" {
		modelUsed = DefaultModel
	}
	usageObj := jsonv.Get(resp, "usage")
	inTok, _ := jsonv.F64(jsonv.Get(usageObj, "input_tokens"))
	outTok, _ := jsonv.F64(jsonv.Get(usageObj, "output_tokens"))

	var (
		weightedSum float64
		totalWeight float64
		penalties   float64
		dims        = make([]DimensionScore, 0, len(scoreSpecs)+len(noulSpecs))
		blockers    []string
		warnings    []string
	)

	for _, s := range scoreSpecs {
		ans := jsonv.Obj(jsonv.Get(answers, s.key))
		if ans == nil {
			return nil, fmt.Errorf("TypeSafe response missing score answer %q", s.key)
		}
		rawScore, ok := jsonv.F64(jsonv.Get(ans, "score"))
		if !ok {
			return nil, fmt.Errorf("TypeSafe answer %q missing numeric `score`", s.key)
		}
		maxLevel := float64(max(1, len(s.levels)-1))
		norm := clamp01(rawScore / maxLevel)
		weightedSum += s.weight * norm
		totalWeight += s.weight

		pct := int(math.Round(norm * 100))
		status := "good"
		switch {
		case s.criticalMin > 0 && norm < s.criticalMin:
			status = "fail"
			penalties += 10.0
			blockers = append(blockers, fmt.Sprintf("%s (%d/100): %s", s.label, pct, s.failFix))
		case norm < 0.50:
			status = "fail"
			warnings = append(warnings, fmt.Sprintf("%s (%d/100): %s", s.label, pct, s.failFix))
		case norm < 0.68:
			status = "warn"
			if s.warnFix != "" {
				warnings = append(warnings, fmt.Sprintf("%s (%d/100): %s", s.label, pct, s.warnFix))
			}
		}
		dims = append(dims, DimensionScore{
			Key:    s.key,
			Label:  s.label,
			Score:  pct,
			Status: status,
		})
	}

	for _, n := range noulSpecs {
		ans := jsonv.Obj(jsonv.Get(answers, n.key))
		if ans == nil {
			return nil, fmt.Errorf("TypeSafe response missing noul answer %q", n.key)
		}
		defectProb, ok := jsonv.F64(jsonv.Get(ans, "noul"))
		if !ok {
			return nil, fmt.Errorf("TypeSafe answer %q missing numeric `noul`", n.key)
		}
		defectProb = clamp01(defectProb)
		cleanNorm := 1.0 - defectProb
		weightedSum += n.weight * cleanNorm
		totalWeight += n.weight

		pct := int(math.Round(cleanNorm * 100))
		status := "good"
		switch {
		case defectProb >= NoulYes:
			status = "fail"
			penalties += n.penalty
			blockers = append(blockers, fmt.Sprintf("%s (%d/100, defect p=%.2f): %s", n.label, pct, defectProb, n.failFix))
		case defectProb > NoulNo:
			status = "warn"
			if n.warnFix != "" {
				warnings = append(warnings, fmt.Sprintf("%s (%d/100, uncertain p=%.2f): %s", n.label, pct, defectProb, n.warnFix))
			}
		}
		dims = append(dims, DimensionScore{
			Key:    n.key,
			Label:  n.label,
			Score:  pct,
			Status: status,
		})
	}

	if totalWeight <= 0 {
		totalWeight = 1
	}
	composite := (weightedSum/totalWeight)*100.0 - penalties
	scoreInt := int(math.Round(composite))
	if scoreInt < 0 {
		scoreInt = 0
	}
	if scoreInt > 100 {
		scoreInt = 100
	}

	verdict := "pass"
	if len(blockers) > 0 || scoreInt < passScore {
		verdict = "revise"
	}

	return &Decision{
		Model:      modelUsed,
		Score:      scoreInt,
		PassScore:  passScore,
		Verdict:    verdict,
		Dimensions: dims,
		Blockers:   blockers,
		Warnings:   warnings,
		Usage: Usage{
			InputTokens:  int(math.Round(inTok)),
			OutputTokens: int(math.Round(outTok)),
		},
	}, nil
}

func guidanceForDimensionKey(key string) string {
	for _, s := range scoreSpecs {
		if s.key == key {
			if s.warnFix != "" {
				return s.warnFix
			}
			return s.failFix
		}
	}
	for _, n := range noulSpecs {
		if n.key == key {
			if n.warnFix != "" {
				return n.warnFix
			}
			return n.failFix
		}
	}
	return "Refine visual polish, spacing rhythm, and contrast on this dimension."
}

func issueLabelPrefix(issue string) string {
	if idx := len(issue); idx > 0 {
		for i := 0; i+1 < len(issue); i++ {
			if issue[i] == ' ' && issue[i+1] == '(' {
				return issue[:i]
			}
		}
	}
	return ""
}

// ActionableIssues returns the prioritized list of concrete visual fixes when Verdict == "revise".
// Blockers always come first; when no hard blockers fired on a threshold-driven revision, it ranks the weakest
// sub-dimensions below PassScore together with any warnings (deduplicated by dimension).
func (d *Decision) ActionableIssues() []string {
	if d == nil || d.Verdict == "pass" {
		return nil
	}
	var out []string
	seenLabels := map[string]bool{}
	addIssue := func(s string) {
		if s == "" {
			return
		}
		lbl := issueLabelPrefix(s)
		if lbl != "" {
			if seenLabels[lbl] {
				return
			}
			seenLabels[lbl] = true
		}
		out = append(out, s)
	}

	for _, b := range d.Blockers {
		addIssue(b)
	}

	if len(d.Blockers) == 0 && len(d.Dimensions) > 0 {
		warnByLabel := map[string]string{}
		for _, w := range d.Warnings {
			if lbl := issueLabelPrefix(w); lbl != "" && warnByLabel[lbl] == "" {
				warnByLabel[lbl] = w
			}
		}
		sorted := make([]DimensionScore, len(d.Dimensions))
		copy(sorted, d.Dimensions)
		for i := 0; i < len(sorted); i++ {
			for j := i + 1; j < len(sorted); j++ {
				if sorted[j].Score < sorted[i].Score {
					sorted[i], sorted[j] = sorted[j], sorted[i]
				}
			}
		}
		for _, dm := range sorted {
			if dm.Score >= d.PassScore && (len(out) > 0 || len(d.Warnings) > 0) {
				break
			}
			if w, ok := warnByLabel[dm.Label]; ok {
				addIssue(w)
			} else {
				addIssue(fmt.Sprintf("%s (%d/100, target ≥ %d): %s", dm.Label, dm.Score, d.PassScore, guidanceForDimensionKey(dm.Key)))
			}
			if len(out) >= 4 {
				break
			}
		}
	}

	for _, w := range d.Warnings {
		if len(out) >= 4 {
			break
		}
		addIssue(w)
	}

	if len(out) == 0 {
		out = append(out, fmt.Sprintf("Composite visual score %d/100 is below revision threshold %d/100; sharpen hierarchy, contrast, color harmony, and spacing.", d.Score, d.PassScore))
	}
	return out
}
