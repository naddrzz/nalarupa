package agent

import (
	"strings"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

func TestDesignHarnessAuditCatchesClippedGridAndSurfaceDrift(t *testing.T) {
	e := engine.New(t.TempDir())
	e.LoadTemplate("empty")
	Attach(e)

	runStep := func(src string) string {
		t.Helper()
		res := callTool(e, "execute", engine.Object("input", src), true, "main")
		if jsonv.IsTrue(jsonv.Get(res, "isError")) {
			t.Fatalf("execute failed: %s", string(jsonv.Marshal(res)))
		}
		items := jsonv.Items(jsonv.Get(res, "content"))
		if len(items) == 0 {
			return ""
		}
		return jsonv.StrOr(jsonv.Get(items[0], "text"))
	}

	// Step 1: Set semantic variables, reusable header chrome, and Page 1 with a textured dot-grid surface layer.
	runStep(`
		SetVariables({
			ground: { type: "color", value: "#F5EFE4" },
			forest: { type: "color", value: "#1E3A2F" },
			ink:    { type: "color", value: "#18221D" },
			accent: { type: "color", value: "#B63B24" }
		});
		navComp = Insert(document, {
			type: "frame", name: "TopNav", reusable: true, x: 0, y: -160,
			width: 1440, padding: [20, 64], justifyContent: "space_between", alignItems: "center", fill: "$forest"
		});
		Insert(navComp, { type: "text", name: "Brand", content: "TALLOW & INK", fontSize: 18, fontWeight: "700", fill: "$ground" });

		p1 = Insert(document, {
			type: "frame", name: "Page · Home", x: 0, y: 0, width: 1440, height: 960,
			layout: "vertical", fill: "$ground", clip: true
		});
		Insert(p1, { type: "ref", ref: navComp, name: "HomeNav", width: "fill_container" });
		paperBg1 = Insert(p1, {
			type: "rectangle", name: "DotGridSurface", layoutPosition: "absolute",
			x: 0, y: 80, width: 1440, height: 880,
			fill: { type: "gradient", kind: "radial", stops: [{ color: "#18221D14", position: 0 }, { color: "#18221D00", position: 1 }] }
		});
	`)

	// Step 2: Create Page 2 ("Page · Shelves") missing the reusable nav & surface treatment,
	// with a padded Body and a horizontal fit_content Row.
	runStep(`
		p2 = Insert(document, {
			type: "frame", name: "Page · Shelves", x: 1560, y: 0, width: 1440, height: 960,
			layout: "vertical", fill: "#F5EFE4", clip: true
		});
		body2 = Insert(p2, {
			type: "frame", name: "Body", width: "fill_container", layout: "vertical", padding: [48, 64], gap: 24
		});
		row1 = Insert(body2, {
			type: "frame", name: "BookRow", layout: "horizontal", gap: 24
		});
	`)

	// Step 3: Insert 4 fixed-width 340px cards into row1 in a separate execute call!
	// 64 (left pad) + 4*340 + 3*24 = 1496 > 1440, so Card 4 overflows and is clipped at the right edge of p2!
	out3 := runStep(`
		Insert(row1, { type: "frame", name: "Card 1", width: 340, height: 420, fill: "#1E3A2F" });
		Insert(row1, { type: "frame", name: "Card 2", width: 340, height: 420, fill: "#B63B24" });
		Insert(row1, { type: "frame", name: "Card 3", width: 340, height: 420, fill: "#1F2E4A" });
		Insert(row1, { type: "frame", name: "Card 4", width: 340, height: 420, fill: "#CAA55E" });
	`)

	if !strings.Contains(out3, "## Issues detected") {
		t.Fatalf("expected Issues detected in Step 3 report, got:\n%s", out3)
	}
	if !strings.Contains(out3, "Card 4") && !strings.Contains(out3, "BookRow") {
		t.Fatalf("expected horizontal overflow / clipping warning naming Card 4 or BookRow, got:\n%s", out3)
	}
	if !strings.Contains(out3, "fill_container") {
		t.Fatalf("expected actionable fill_container guidance in overflow warning, got:\n%s", out3)
	}
	if !strings.Contains(out3, "Page · Shelves") {
		t.Fatalf("expected multi-screen consistency warning for Page · Shelves vs Page · Home, got:\n%s", out3)
	}
	for _, wantSub := range []string{"omits reusable page chrome", "has a flat", "hardcodes fill"} {
		if !strings.Contains(out3, wantSub) {
			t.Fatalf("expected audit report to contain %q, got:\n%s", wantSub, out3)
		}
	}
}
