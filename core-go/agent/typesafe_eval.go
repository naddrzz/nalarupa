package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/typesafe"
)

const typeSafeToolName = "typesafe_decision"

type typeSafeRuntimeCfg struct {
	enabled    bool
	autoRevise bool
	maxRevs    int
	apiKey     string
	baseURL    string
	model      string
	minScore   int
	cancel     *engine.Flag
}

func snapshotTypeSafeConfig(e *engine.Engine) typeSafeRuntimeCfg {
	var cfg typeSafeRuntimeCfg
	if e == nil {
		return cfg
	}
	e.Locked(func() {
		cfg.enabled = e.TypeSafeEnabled()
		cfg.autoRevise = e.TypeSafeAutoRevise()
		cfg.maxRevs = e.TypeSafeEffectiveMaxRevisions()
		cfg.apiKey, _ = e.TypeSafeAPIKey()
		cfg.baseURL = e.TypeSafeBaseURL()
		cfg.model = e.TypeSafeModel()
		cfg.minScore = e.TypeSafeMinScore()
		cfg.cancel = e.Agent.Cancel
	})
	return cfg
}

func contextForFlag(flag *engine.Flag) (context.Context, context.CancelFunc) {
	ctx, cancelFn := context.WithCancel(context.Background())
	if flag == nil {
		return ctx, cancelFn
	}
	if flag.IsSet() {
		cancelFn()
		return ctx, cancelFn
	}
	go func() {
		select {
		case <-flag.Done():
			cancelFn()
		case <-ctx.Done():
		}
	}()
	return ctx, cancelFn
}

func formatDimensionSummary(dims []typesafe.DimensionScore) string {
	parts := make([]string, 0, len(dims))
	for _, d := range dims {
		parts = append(parts, fmt.Sprintf("%s %d", d.Label, d.Score))
	}
	return strings.Join(parts, " · ")
}

func formatDecisionReport(screenName, screenID string, dec *typesafe.Decision, attempt, maxRevs int, autoRevise bool) string {
	var b strings.Builder
	badge := "PASS"
	if dec.Verdict != "pass" {
		if autoRevise && attempt > 0 && maxRevs > 0 {
			badge = fmt.Sprintf("AUTO-REVISE %d/%d", attempt, maxRevs)
		} else {
			badge = fmt.Sprintf("BELOW %d THRESHOLD", dec.PassScore)
		}
	}
	fmt.Fprintf(&b, "Screen %q (%s) — TypeSafe Visual Score: %d/100 [%s, threshold %d/100, model %s]\n",
		screenName, screenID, dec.Score, badge, dec.PassScore, dec.Model)
	if len(dec.Dimensions) > 0 {
		fmt.Fprintf(&b, "Breakdown: %s\n", formatDimensionSummary(dec.Dimensions))
	}
	issues := dec.ActionableIssues()
	if len(issues) > 0 {
		b.WriteString("\nActionable visual findings:\n")
		for _, iss := range issues {
			fmt.Fprintf(&b, "- %s\n", iss)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func decisionMetaJSON(screenName, screenID string, dec *typesafe.Decision, attempt, maxRevs int, autoRevise bool) *jsonv.Object {
	dims := jsonv.NewArray()
	dims.Items = make([]jsonv.Value, 0, len(dec.Dimensions))
	for _, d := range dec.Dimensions {
		dims.Items = append(dims.Items, engine.Object(
			"key", d.Key,
			"label", d.Label,
			"score", jsonv.Int(int64(d.Score)),
			"status", d.Status,
		))
	}
	issues := dec.ActionableIssues()
	return engine.Object(
		"screenId", screenID,
		"screenName", screenName,
		"score", jsonv.Int(int64(dec.Score)),
		"passScore", jsonv.Int(int64(dec.PassScore)),
		"verdict", dec.Verdict,
		"autoRevise", autoRevise,
		"revisionAttempt", jsonv.Int(int64(attempt)),
		"maxRevisions", jsonv.Int(int64(maxRevs)),
		"model", dec.Model,
		"dimensions", dims,
		"issues", engine.Strings(issues),
	)
}

func emitTypeSafeStart(e *engine.Engine, p *engine.Profile, evalID, screenName, screenID string) {
	if p == nil {
		return
	}
	emit(e, *p, "tool_start", engine.Object(
		"id", evalID,
		"name", typeSafeToolName,
		"screenName", screenName,
		"screenId", screenID,
	))
}

func emitTypeSafeDone(e *engine.Engine, p *engine.Profile, evalID, screenName, screenID, output string, images []string, isError bool, tsMeta *jsonv.Object, issues []string) {
	if p == nil {
		return
	}
	inObj := engine.Object("screen", screenName, "screenId", screenID)
	ev := engine.Object(
		"id", evalID,
		"name", typeSafeToolName,
		"input", inObj,
		"output", truncate(output, 6000),
		"images", engine.Strings(images[:min(1, len(images))]),
		"error", isError,
		"issues", engine.Strings(issues),
		"t", u64(engine.NowMs()),
	)
	if tsMeta != nil {
		ev.Set("typesafe", tsMeta)
	}
	emit(e, *p, "tool_done", ev)
}

func emitAutoReviseNote(e *engine.Engine, p *engine.Profile, screenName string, score, passScore, attempt, maxRevs int) {
	if p == nil {
		return
	}
	emit(e, *p, "note", engine.Object(
		"text", fmt.Sprintf("Auto-revising %q (%d/100 < %d/100 threshold · pass %d/%d)…", screenName, score, passScore, attempt, maxRevs),
	))
}

// evaluateSingleScreenPlan renders ONLY `plan.ID` to PNG, prepares it within TypeSafe's bounds, calls Jev,
// and emits the UI score card. Must be called WITHOUT holding Engine.Lock.
func evaluateSingleScreenPlan(
	ctx context.Context,
	e *engine.Engine,
	prof *engine.Profile,
	client *typesafe.Client,
	minScore int,
	plan engine.ScreenEvalPlan,
	consumeRevision bool,
) (dec *typesafe.Decision, attempt int, maxRevs int, canRevise bool, err error) {
	evalID := fmt.Sprintf("ts_%x_%s", engine.NowMs(), plan.ID)
	emitTypeSafeStart(e, prof, evalID, plan.Name, plan.ID)

	rawPNG, rastErr := e.RasterizeScreenPlan(plan)
	if rastErr != nil {
		msg := fmt.Sprintf("Could not render screen %q (%s) for TypeSafe AI evaluation: %s", plan.Name, plan.ID, rastErr)
		emitTypeSafeDone(e, prof, evalID, plan.Name, plan.ID, msg, nil, true, nil, nil)
		return nil, 0, 0, false, rastErr
	}
	preparedPNG, _, _, prepErr := typesafe.PrepareScreenPNG(rawPNG)
	if prepErr != nil {
		msg := fmt.Sprintf("Could not prepare screen %q (%s) image for TypeSafe AI: %s", plan.Name, plan.ID, prepErr)
		emitTypeSafeDone(e, prof, evalID, plan.Name, plan.ID, msg, nil, true, nil, nil)
		return nil, 0, 0, false, prepErr
	}
	b64Thumb := base64.StdEncoding.EncodeToString(preparedPNG)

	dec, err = client.EvaluateScreen(ctx, preparedPNG, minScore)
	if err != nil {
		// Surface API failures as errors, never as fake scores or silent passes.
		msg := fmt.Sprintf("TypeSafe AI visual check error on screen %q (%s): %s", plan.Name, plan.ID, err)
		emitTypeSafeDone(e, prof, evalID, plan.Name, plan.ID, msg, []string{b64Thumb}, true, nil, nil)
		return nil, 0, 0, false, err
	}

	var sameDoc bool
	e.Locked(func() {
		attempt, maxRevs, canRevise, sameDoc = e.RecordScreenEvaluation(plan.SourceDoc, plan.ID, plan.Hash, dec, consumeRevision)
	})
	if !sameDoc {
		return nil, 0, 0, false, nil
	}
	report := formatDecisionReport(plan.Name, plan.ID, dec, attempt, maxRevs, canRevise)
	meta := decisionMetaJSON(plan.Name, plan.ID, dec, attempt, maxRevs, canRevise)
	emitTypeSafeDone(e, prof, evalID, plan.Name, plan.ID, report, []string{b64Thumb}, false, meta, dec.ActionableIssues())
	if canRevise {
		emitAutoReviseNote(e, prof, plan.Name, dec.Score, dec.PassScore, attempt, maxRevs)
	}
	return dec, attempt, maxRevs, canRevise, nil
}

// RunTurnEndTypeSafeGate evaluates any newly created or edited screens owned by `p` at the end of an agent turn
// whose latest visual state has not been evaluated yet. When auto-revision is enabled and a screen's score is below
// the configured threshold (with revision budget left), it returns a targeted auto-revision directive; otherwise "".
func RunTurnEndTypeSafeGate(e *engine.Engine, p engine.Profile) string {
	if e == nil {
		return ""
	}
	cfg := snapshotTypeSafeConfig(e)
	if !cfg.enabled || cfg.apiKey == "" {
		return ""
	}
	var plans []engine.ScreenEvalPlan
	e.Locked(func() {
		plans = e.PlanTurnTypeSafeEvaluations(p.ID)
	})
	if len(plans) == 0 {
		return ""
	}

	ctx, cancelFn := contextForFlag(cfg.cancel)
	defer cancelFn()
	client := typesafe.NewClient(cfg.apiKey, cfg.baseURL, cfg.model)

	var revisePrompts []string
	for _, plan := range plans {
		if ctx.Err() != nil {
			return ""
		}
		if plan.CachedRevise != nil {
			var attempt, maxRevs int
			var allowed bool
			e.Locked(func() { attempt, maxRevs, allowed = e.ConsumeCachedRevise(plan.SourceDoc, plan.ID) })
			if allowed {
				emitAutoReviseNote(e, &p, plan.Name, plan.CachedRevise.Score, plan.CachedRevise.PassScore, attempt, maxRevs)
				revisePrompts = append(revisePrompts, formatRevisionDirective(plan.Name, plan.ID, plan.CachedRevise, attempt, maxRevs))
			}
			continue
		}
		dec, attempt, maxRevs, canRevise, err := evaluateSingleScreenPlan(ctx, e, &p, client, cfg.minScore, plan, cfg.autoRevise)
		if err != nil || dec == nil {
			continue
		}
		if dec.Verdict != "pass" && canRevise {
			revisePrompts = append(revisePrompts, formatRevisionDirective(plan.Name, plan.ID, dec, attempt, maxRevs))
		}
	}

	if len(revisePrompts) == 0 {
		return ""
	}
	return "TypeSafe AI auto-revision triggered because screen visual score(s) fell below the configured threshold. " +
		"Revise ONLY the flagged screen(s) below using `execute` until their visual quality clears the threshold " +
		"(do NOT modify any other already-finished screens on the canvas), then briefly confirm the fixes:\n\n" +
		strings.Join(revisePrompts, "\n\n")
}

func formatRevisionDirective(screenName, screenID string, dec *typesafe.Decision, attempt, maxRevs int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### Screen %q (`%s`) — Score: %d/100 (threshold ≥ %d/100 · auto-revision %d/%d)\n",
		screenName, screenID, dec.Score, dec.PassScore, attempt, maxRevs)
	fmt.Fprintf(&b, "Sub-scores: %s\n", formatDimensionSummary(dec.Dimensions))
	b.WriteString("Apply concrete visual fixes on this screen:\n")
	for _, iss := range dec.ActionableIssues() {
		fmt.Fprintf(&b, "- %s\n", iss)
	}
	return strings.TrimRight(b.String(), "\n")
}
