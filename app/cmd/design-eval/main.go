// Design agent evaluation CLI (manual: runs spend model tokens, so not run in CI).
//
// Usage:
//   CGO_ENABLED=1 go run ./cmd/design-eval run [--backend claude-code|api|provider] [--model id] [--only id,id] [--designs n --iterate style|layout] [--parallel n [--mode split|side]] [--out dir]
//   CGO_ENABLED=1 go run ./cmd/design-eval compare <runA> <runB> [--out page.html]
//   CGO_ENABLED=1 go run ./cmd/design-eval summary <run>
//   CGO_ENABLED=1 go run ./cmd/design-eval fonts
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"image/png"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/naddrzz/nalarupa/core-go/agent"
	_ "github.com/naddrzz/nalarupa/core-go/editor"
	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/guide"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

type evalBrief struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // "page" | "mobile" | "slides" | "existing"
	Text string `json:"text"`
}

var evalBriefs = []evalBrief{
	{ID: "bakery", Kind: "page", Text: "Landing page for Sourdough & Co, a neighbourhood bakery in Leeds. It opens at 7am, bakes three loaves a day in a wood-fired oven, and takes pre-orders for Saturday pickup."},
	{ID: "ledgerline", Kind: "page", Text: "Landing page for Ledgerline, accounting software for freelance translators: it tracks invoices in several currencies, sets tax aside every month, and exports everything for the annual return."},
	{ID: "docfest", Kind: "page", Text: "One-page site for Olhar, a three-day documentary film festival in Lisbon (12–14 September): programme highlights, the three venues, and ticket prices."},
	{ID: "climb", Kind: "mobile", Text: "Home screen of a mobile app for people learning to climb indoors: today's session plan, progress through the grades, and the next booked wall slot."},
	{ID: "trains", Kind: "mobile", Text: "Mobile app screen that shows a commuter their next trains from Utrecht Centraal to Amsterdam Zuid, with delays and platforms, and a way to save the route."},
	{ID: "roastery", Kind: "slides", Text: "A 5-slide deck for the quarterly review of Halden, a small coffee roastery: revenue for the quarter, the new wholesale clients, a green-coffee sourcing problem, and next quarter's plan."},
	{ID: "bridges", Kind: "slides", Text: "A 5-slide deck for a 10-minute talk to secondary-school students on why bridges don't fall down."},
	{ID: "fernway", Kind: "existing", Text: "Add a pricing page for Fernway next to the home page: three subscription plans and a short FAQ."},
}

const fernwayFixture = `
SetVariables({ink: {type: "color", value: "#1F2A1C"}, paper: {type: "color", value: "#F4EFE6"}, moss: {type: "color", value: "#4F6B3A"}, clay: {type: "color", value: "#C8643B"}, space: {type: "number", value: 24}})
kit = Insert(document, {type: "frame", name: "Components", x: 0, y: -260, layout: "horizontal", gap: 24, padding: 32, fill: "#FFFFFF"})
btn = Insert(kit, {type: "frame", name: "Button", reusable: true, padding: [14, 26], cornerRadius: 999, fill: "$clay", alignItems: "center"})
btnLabel = Insert(btn, {type: "text", name: "Label", content: "Start my plan", fontFamily: "Work Sans", fontSize: 16, fontWeight: "600", fill: "$paper"})
home = Insert(document, {type: "frame", name: "Fernway Home", x: 0, y: 0, width: 1440, layout: "vertical", fill: "$paper", clip: true})
nav = Insert(home, {type: "frame", name: "Nav", width: "fill_container", padding: [28, 96], justifyContent: "space_between", alignItems: "center"})
Insert(nav, {type: "text", name: "Logo", content: "fernway", fontFamily: "Fraunces", fontSize: 30, fontWeight: "600", fill: "$moss"})
links = Insert(nav, {type: "frame", name: "Links", gap: 36, alignItems: "center"})
for (const l of ["How it works", "Plants", "Journal"]) Insert(links, {type: "text", name: l, content: l, fontFamily: "Work Sans", fontSize: 16, fill: "$ink"})
hero = Insert(home, {type: "frame", name: "Hero", width: "fill_container", padding: [96, 96, 120, 96], gap: 64, alignItems: "end"})
copy = Insert(hero, {type: "frame", name: "Copy", width: "fill_container", layout: "vertical", gap: "$space"})
Insert(copy, {type: "text", name: "Kicker", content: "PLANT CARE, POSTED MONTHLY", fontFamily: "Work Sans", fontSize: 14, fontWeight: "600", letterSpacing: 2, fill: "$moss"})
Insert(copy, {type: "text", name: "Headline", content: "Your plants, looked after by people who know them.", textGrowth: "fixed-width", width: "fill_container", fontFamily: "Fraunces", fontSize: 72, lineHeight: 1.05, fill: "$ink"})
Insert(copy, {type: "text", name: "Lede", content: "Every month a Fernway gardener checks your plants over video, then sends the feed, soil and pots they need.", textGrowth: "fixed-width", width: 520, fontFamily: "Work Sans", fontSize: 19, lineHeight: 1.5, fill: "$ink"})
Insert(copy, {type: "ref", ref: btn, name: "Hero CTA"})
Insert(hero, {type: "frame", name: "Photo", width: 520, height: 600, cornerRadius: [260, 260, 0, 0], fill: "$moss"})
steps = Insert(home, {type: "frame", name: "How it works", width: "fill_container", padding: [96, 96], gap: 48, fill: "#EAE3D6"})
for (const [n, t, d] of [["01", "Tell us your plants", "Snap a photo of each one; we identify it and its light."], ["02", "Meet your gardener", "A monthly video check-in with the same person every time."], ["03", "Get the right kit", "Feed, soil and pots arrive sized for your plants."]]) {
  const s = Insert(steps, {type: "frame", name: "Step " + n, width: "fill_container", layout: "vertical", gap: 12})
  Insert(s, {type: "text", name: "Number", content: n, fontFamily: "Fraunces", fontSize: 40, fill: "$clay"})
  Insert(s, {type: "text", name: "Title", content: t, fontFamily: "Work Sans", fontSize: 22, fontWeight: "600", fill: "$ink"})
  Insert(s, {type: "text", name: "Body", content: d, textGrowth: "fixed-width", width: "fill_container", fontFamily: "Work Sans", fontSize: 17, lineHeight: 1.5, fill: "$ink"})
}
`

const (
	sameColourThreshold = 0.83
	sameLayoutThreshold = 0.85
	sameGroundThreshold = 12.0
)

var kindAspect = map[string]float64{
	"page":     0.625,
	"mobile":   2.16,
	"slides":   0.5625,
	"existing": 0.625,
}

type evalShot struct {
	File string `json:"file"`
	Name string `json:"name"`
	W    int    `json:"w"`
	H    int    `json:"h"`
}

type evalBriefResult struct {
	ID                string   `json:"id"`
	Kind              string   `json:"kind"`
	Brief             string   `json:"brief"`
	OK                bool     `json:"ok"`
	Error             string   `json:"error,omitempty"`
	WallMS            int64    `json:"wallMs"`
	Turns             int      `json:"turns"`
	CostUSD           float64  `json:"costUsd"`
	ExecuteCalls      int      `json:"executeCalls"`
	FailedExecutes    int      `json:"failedExecutes"`
	Repairs           int      `json:"repairs"`
	Issues            []string `json:"issues"`
	Screenshots       int      `json:"screenshots"`
	InstructionsChars int      `json:"instructionsChars"`
	FirstMessage      string   `json:"firstMessage"`
	Shots             []evalShot `json:"shots"`
}

type evalRun struct {
	Meta struct {
		SHA      string `json:"sha"`
		Backend  string `json:"backend"`
		Model    string `json:"model,omitempty"`
		Designs  int    `json:"designs,omitempty"`
		Iterate  string `json:"iterate,omitempty"`
		Parallel int    `json:"parallel,omitempty"`
		Mode     string `json:"mode,omitempty"`
		Date     string `json:"date"`
	} `json:"meta"`
	Briefs []evalBriefResult `json:"briefs"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = cmdRun(os.Args[2:])
	case "compare":
		err = cmdCompare(os.Args[2:])
	case "summary":
		err = cmdSummary(os.Args[2:])
	case "fonts":
		err = cmdFonts()
	default:
		usage()
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "design-eval: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage:
  design-eval run [--backend claude-code|api|provider] [--model id] [--only id,id] [--designs n --iterate style|layout] [--parallel n [--mode split|side]] [--out dir]
  design-eval compare <runA> <runB> [--out page.html]
  design-eval summary <runDir>
  design-eval fonts`)
}

func flagVal(args []string, name string) (string, bool, error) {
	idx := slices.Index(args, "--"+name)
	if idx < 0 {
		return "", false, nil
	}
	if idx+1 >= len(args) || strings.HasPrefix(args[idx+1], "--") {
		return "", false, fmt.Errorf("--%s needs a value", name)
	}
	return args[idx+1], true, nil
}

func dispatch(eng *engine.Engine, cmd string, args map[string]any, out any) error {
	m := map[string]any{"cmd": cmd}
	for k, v := range args {
		m[k] = v
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	v, err := jsonv.Parse(raw)
	if err != nil {
		return err
	}
	res, err := eng.Dispatch(v)
	if err != nil {
		return err
	}
	if out != nil {
		return json.Unmarshal(jsonv.Marshal(res), out)
	}
	return nil
}

func cmdFonts() error {
	tmp, err := os.MkdirTemp("", "nalarupa-eval-fonts-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	eng := engine.New(tmp)
	agent.Attach(eng)
	defer agent.Shutdown(eng)

	var initRes map[string]any
	if err := dispatch(eng, "init", nil, &initRes); err != nil {
		return err
	}
	if err := dispatch(eng, "new", map[string]any{"template": "empty"}, nil); err != nil {
		return err
	}

	// Extract font family names quoted in design guides
	var guideText strings.Builder
	guideText.WriteString(guide.Designing)
	for _, topic := range []string{"design", "slides", "schema"} {
		if s, ok := guide.Read(topic); ok {
			guideText.WriteString("\n" + s)
		}
	}
	reFont := regexp.MustCompile(`fontFamily:\s*"([^"]+)"`)
	seen := map[string]bool{"Inter": true}
	for _, m := range reFont.FindAllStringSubmatch(guideText.String(), -1) {
		f := strings.TrimSpace(m[1])
		if f != "" && !strings.HasPrefix(f, "$") {
			seen[f] = true
		}
	}
	var families []string
	for f := range seen {
		families = append(families, f)
	}
	slices.Sort(families)
	for _, fam := range families {
		_ = dispatch(eng, "insert", map[string]any{
			"parent": "document",
			"node": map[string]any{
				"type":       "text",
				"content":    "Sample",
				"fontFamily": fam,
				"fontSize":   24,
			},
		}, nil)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		allReady := true
		for _, fam := range families {
			var st struct {
				Available bool `json:"available"`
				Loading   bool `json:"loading"`
			}
			if err := dispatch(eng, "font_state", map[string]any{"family": fam}, &st); err != nil {
				return err
			}
			if st.Loading {
				allReady = false
			}
		}
		if allReady {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	var unavailable []string
	for _, fam := range families {
		var st struct {
			Available bool `json:"available"`
			Loading   bool `json:"loading"`
		}
		if err := dispatch(eng, "font_state", map[string]any{"family": fam}, &st); err != nil {
			return err
		}
		if st.Loading || !st.Available {
			unavailable = append(unavailable, fam)
		}
	}
	if len(unavailable) > 0 {
		return fmt.Errorf("unavailable fonts: %s", strings.Join(unavailable, ", "))
	}
	fmt.Printf("fonts checked (%d families)\n", len(families))
	return nil
}

func cmdSummary(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("summary requires <runDir>")
	}
	dir := args[0]
	raw, err := os.ReadFile(filepath.Join(dir, "metrics.json"))
	if err != nil {
		return err
	}
	var run evalRun
	if err := json.Unmarshal(raw, &run); err != nil {
		return err
	}
	md := buildSummaryMarkdown(dir, run)
	if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte(md), 0o644); err != nil {
		return err
	}
	fmt.Print(md)
	return nil
}

func cmdRun(args []string) error {
	backend, ok, err := flagVal(args, "backend")
	if err != nil {
		return err
	}
	if !ok {
		backend = "claude-code"
	}
	if backend != "claude-code" && backend != "api" && backend != "provider" {
		return fmt.Errorf("unknown backend %q: claude-code, api or provider", backend)
	}
	model, hasModel, err := flagVal(args, "model")
	if err != nil {
		return err
	}
	onlyStr, hasOnly, err := flagVal(args, "only")
	if err != nil {
		return err
	}
	var selected []evalBrief
	if hasOnly {
		ids := strings.Split(onlyStr, ",")
		for _, id := range ids {
			idx := slices.IndexFunc(evalBriefs, func(b evalBrief) bool { return b.ID == id })
			if idx < 0 {
				return fmt.Errorf("unknown eval brief %q", id)
			}
			selected = append(selected, evalBriefs[idx])
		}
	} else {
		selected = evalBriefs
	}

	designs := 1
	if s, ok, err := flagVal(args, "designs"); err != nil {
		return err
	} else if ok {
		designs, _ = strconv.Atoi(s)
	}
	iterate := "style"
	if s, ok, err := flagVal(args, "iterate"); err != nil {
		return err
	} else if ok {
		iterate = s
	}
	parallel := 1
	if s, ok, err := flagVal(args, "parallel"); err != nil {
		return err
	} else if ok {
		parallel, _ = strconv.Atoi(s)
	}
	mode := "split"
	if s, ok, err := flagVal(args, "mode"); err != nil {
		return err
	} else if ok {
		mode = s
	}

	outDir, hasOut, err := flagVal(args, "out")
	if err != nil {
		return err
	}
	if !hasOut {
		if envOut := os.Getenv("EVAL_OUT"); envOut != "" {
			outDir = envOut
		} else {
			home, _ := os.UserHomeDir()
			outDir = filepath.Join(home, "nalarupa-eval", time.Now().Format("20060102-150405"))
		}
	}
	outDir, err = filepath.Abs(outDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	tmpData, err := os.MkdirTemp("", "nalarupa-eval-data-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpData)

	eng := engine.New(tmpData)
	agent.Attach(eng)
	defer agent.Shutdown(eng)

	var initRes struct {
		MCPPort int `json:"mcpPort"`
	}
	if err := dispatch(eng, "init", nil, &initRes); err != nil {
		return err
	}
	if initRes.MCPPort == 0 {
		return fmt.Errorf("MCP server did not start")
	}

	if err := dispatch(eng, "settings_set", map[string]any{"key": "agent_backend", "value": backend}, nil); err != nil {
		return err
	}
	if hasModel {
		key := "agent_model"
		if backend == "provider" {
			key = "provider_model"
		}
		if err := dispatch(eng, "settings_set", map[string]any{"key": key, "value": model}, nil); err != nil {
			return err
		}
	}

	shaBytes, _ := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	var run evalRun
	run.Meta.SHA = strings.TrimSpace(string(shaBytes))
	run.Meta.Backend = backend
	run.Meta.Model = model
	if designs > 1 {
		run.Meta.Designs = designs
		run.Meta.Iterate = iterate
	}
	if parallel > 1 {
		run.Meta.Parallel = parallel
		run.Meta.Mode = mode
	}
	run.Meta.Date = time.Now().UTC().Format("2006-01-02 15:04")

	var instr string
	_ = dispatch(eng, "agent_instructions", map[string]any{"backend": backend}, &instr)

	for _, b := range selected {
		bDir := filepath.Join(outDir, b.ID)
		if err := os.MkdirAll(bDir, 0o755); err != nil {
			return err
		}

		br := evalBriefResult{
			ID:                b.ID,
			Kind:              b.Kind,
			Brief:             b.Text,
			OK:                true,
			Issues:            []string{},
			InstructionsChars: len(instr),
		}
		fmt.Printf("== %s\n", b.ID)

		if err := dispatch(eng, "agent_reset", nil, nil); err != nil {
			_ = dispatch(eng, "agent_stop", nil, nil)
			if resetErr := dispatch(eng, "agent_reset", nil, nil); resetErr != nil {
				br.OK = false
				br.Error = resetErr.Error()
				run.Briefs = append(run.Briefs, br)
				continue
			}
		}
		if err := dispatch(eng, "new", map[string]any{"template": "empty"}, nil); err != nil {
			br.OK = false
			br.Error = err.Error()
			run.Briefs = append(run.Briefs, br)
			continue
		}

		if b.Kind == "existing" {
			if err := callMCPExecute(initRes.MCPPort, fernwayFixture); err != nil {
				br.OK = false
				br.Error = err.Error()
				run.Briefs = append(run.Briefs, br)
				continue
			}
			time.Sleep(4 * time.Second)
		}

		var beforeFrames []struct {
			ID string `json:"id"`
		}
		_ = dispatch(eng, "frames", nil, &beforeFrames)
		beforeSet := map[string]bool{}
		for _, f := range beforeFrames {
			beforeSet[f.ID] = true
		}

		start := time.Now()
		if err := dispatch(eng, "agent_send", map[string]any{
			"text":     b.Text,
			"parallel": parallel,
			"mode":     mode,
			"designs":  designs,
			"iterate":  iterate,
		}, nil); err != nil {
			br.OK = false
			br.Error = err.Error()
			run.Briefs = append(run.Briefs, br)
			continue
		}

		var preExec strings.Builder
		seenExec := false
		finished := false
		deadline := time.Now().Add(30 * time.Minute)
		for time.Now().Before(deadline) {
			time.Sleep(250 * time.Millisecond)
			var poll struct {
				State struct {
					AgentRunning bool `json:"agentRunning"`
				} `json:"state"`
				Events []map[string]any `json:"events"`
			}
			if err := dispatch(eng, "poll", nil, &poll); err != nil {
				br.OK = false
				br.Error = "poll failed: " + err.Error()
				_ = dispatch(eng, "agent_stop", nil, nil)
				break
			}
			for _, ev := range poll.Events {
				if ev["type"] != "agent" {
					continue
				}
				switch ev["kind"] {
				case "text_delta":
					if !seenExec {
						s, _ := ev["text"].(string)
						preExec.WriteString(s)
					}
				case "tool_done":
					name, _ := ev["name"].(string)
					if strings.HasSuffix(name, "execute") {
						seenExec = true
						br.ExecuteCalls++
						if inp, ok := ev["input"].(map[string]any); ok && inp["editId"] != nil {
							br.Repairs++
						}
						if isErr, _ := ev["error"].(bool); isErr {
							br.FailedExecutes++
						}
						if outTxt, _ := ev["output"].(string); outTxt != "" {
							br.Issues = append(br.Issues, extractExecuteIssues(outTxt)...)
						}
						if imgs, ok := ev["images"].([]any); ok && len(imgs) > 0 {
							br.Screenshots++
						}
					}
				case "error":
					br.OK = false
					br.Error, _ = ev["text"].(string)
				case "done":
					if u, ok := ev["usage"].(map[string]any); ok {
						if t, ok := u["turns"].(float64); ok {
							br.Turns += int(t)
						}
						if c, ok := u["costUsd"].(float64); ok {
							br.CostUSD += c
						}
					}
				}
			}
			if !poll.State.AgentRunning {
				finished = true
				break
			}
		}
		if !finished {
			_ = dispatch(eng, "agent_stop", nil, nil)
			br.OK = false
			if br.Error == "" {
				br.Error = "agent timed out"
			}
		}
		br.WallMS = time.Since(start).Milliseconds()
		br.FirstMessage = strings.TrimSpace(preExec.String())

		if err := dispatch(eng, "save", map[string]any{"path": filepath.Join(bDir, b.ID+".nala")}, nil); err != nil {
			br.OK = false
			if br.Error == "" {
				br.Error = "save failed: " + err.Error()
			}
		}

		var afterFrames []struct {
			ID   string  `json:"id"`
			Name string  `json:"name"`
			Type string  `json:"type"`
			W    float64 `json:"w"`
			H    float64 `json:"h"`
		}
		_ = dispatch(eng, "frames", nil, &afterFrames)
		for idx, f := range afterFrames {
			if beforeSet[f.ID] || f.Type != "frame" {
				continue
			}
			shotFile := fmt.Sprintf("%02d-%s.png", idx+1, f.ID)
			var files []string
			if err := dispatch(eng, "export", map[string]any{
				"ids":    []string{f.ID},
				"format": "png",
				"path":   filepath.Join(bDir, shotFile),
				"opts":   map[string]any{"scale": 1},
			}, &files); err == nil && len(files) > 0 {
				br.Shots = append(br.Shots, evalShot{
					File: filepath.Join(b.ID, shotFile),
					Name: f.Name,
					W:    int(math.Round(f.W)),
					H:    int(math.Round(f.H)),
				})
			}
		}
		run.Briefs = append(run.Briefs, br)
	}

	metricsBytes, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "metrics.json"), metricsBytes, 0o644); err != nil {
		return err
	}
	summaryMD := buildSummaryMarkdown(outDir, run)
	if err := os.WriteFile(filepath.Join(outDir, "summary.md"), []byte(summaryMD), 0o644); err != nil {
		return err
	}
	fmt.Println(summaryMD)
	fmt.Printf("Saved eval run to %s\n", outDir)
	return nil
}

func extractExecuteIssues(output string) []string {
	var issues []string
	inIssues := false
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## Issues detected") {
			inIssues = true
			continue
		}
		if inIssues {
			if strings.HasPrefix(trimmed, "## ") {
				break
			}
			if trimmed == "" {
				continue
			}
			issues = append(issues, strings.TrimPrefix(trimmed, "- "))
		}
	}
	return issues
}

func callMCPExecute(port int, code string) error {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "execute",
			"arguments": map[string]any{"input": code},
		},
	})
	resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/mcp", port), "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var rpc struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpc); err != nil {
		return err
	}
	if rpc.Result.IsError {
		var sb strings.Builder
		for _, c := range rpc.Result.Content {
			sb.WriteString(c.Text)
		}
		return fmt.Errorf("%s", sb.String())
	}
	return nil
}

type thumbGrid struct {
	w  int
	h  int
	px []float64
}

func computeThumb(file, kind string) (*thumbGrid, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	iw, ih := b.Dx(), b.Dy()
	if iw <= 0 || ih <= 0 {
		return nil, fmt.Errorf("empty image")
	}
	aspect := kindAspect[kind]
	if aspect <= 0 {
		aspect = 0.625
	}
	cropH := min(float64(ih), float64(iw)*aspect)
	gw := 16
	gh := max(1, int(math.Round(16.0*aspect)))
	px := make([]float64, 0, gw*gh*3)
	for gy := 0; gy < gh; gy++ {
		y0 := int(math.Floor((float64(gy) * cropH) / float64(gh)))
		y1 := max(y0+1, int(math.Floor((float64(gy+1)*cropH)/float64(gh))))
		for gx := 0; gx < gw; gx++ {
			x0 := int(math.Floor((float64(gx) * float64(iw)) / float64(gw)))
			x1 := max(x0+1, int(math.Floor((float64(gx+1)*float64(iw))/float64(gw))))
			var sr, sg, sb float64
			n := 0
			for y := y0; y < y1 && y < ih; y++ {
				for x := x0; x < x1 && x < iw; x++ {
					r16, g16, b16, a16 := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
					r := float64(r16 >> 8)
					g := float64(g16 >> 8)
					bl := float64(b16 >> 8)
					k := float64(a16>>8) / 255.0
					sr += r*k + 255*(1-k)
					sg += g*k + 255*(1-k)
					sb += bl*k + 255*(1-k)
					n++
				}
			}
			div := float64(max(1, n))
			px = append(px, sr/div, sg/div, sb/div)
		}
	}
	return &thumbGrid{w: gw, h: gh, px: px}, nil
}

func thumbSimilarity(a, b *thumbGrid) (colour, layout float64) {
	if a == nil || b == nil || len(a.px) == 0 || len(a.px) != len(b.px) {
		return 0, 0
	}
	sumDiff := 0.0
	for i := range a.px {
		sumDiff += math.Abs(a.px[i] - b.px[i])
	}
	colour = 1.0 - sumDiff/float64(len(a.px))/255.0

	cells := len(a.px) / 3
	lx := make([]float64, cells)
	ly := make([]float64, cells)
	var mx, my float64
	for i := 0; i < cells; i++ {
		lx[i] = (a.px[i*3] + a.px[i*3+1] + a.px[i*3+2]) / 3.0
		ly[i] = (b.px[i*3] + b.px[i*3+1] + b.px[i*3+2]) / 3.0
		mx += lx[i]
		my += ly[i]
	}
	mx /= float64(cells)
	my /= float64(cells)
	var xy, xx, yy float64
	for i := 0; i < cells; i++ {
		dx := lx[i] - mx
		dy := ly[i] - my
		xy += dx * dy
		xx += dx * dx
		yy += dy * dy
	}
	if xx > 0 && yy > 0 {
		layout = max(0.0, xy/math.Sqrt(xx*yy))
	}
	return colour, layout
}

func dominantGroundHex(file, kind string) ([3]float64, string, error) {
	f, err := os.Open(file)
	if err != nil {
		return [3]float64{}, "", err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return [3]float64{}, "", err
	}
	b := img.Bounds()
	iw, ih := b.Dx(), b.Dy()
	aspect := kindAspect[kind]
	if aspect <= 0 {
		aspect = 0.625
	}
	cropH := int(min(float64(ih), float64(iw)*aspect))
	step := max(1, int(math.Floor(math.Sqrt(float64(iw*cropH)/40000.0))))
	type binAcc struct {
		r, g, b float64
		count   int
	}
	bins := map[int]*binAcc{}
	for y := 0; y < cropH; y += step {
		for x := 0; x < iw; x += step {
			r16, g16, b16, a16 := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			k := float64(a16>>8) / 255.0
			pr := float64(r16>>8)*k + 255*(1-k)
			pg := float64(g16>>8)*k + 255*(1-k)
			pb := float64(b16>>8)*k + 255*(1-k)
			key := ((int(pr) >> 3) << 10) | ((int(pg) >> 3) << 5) | (int(pb) >> 3)
			acc := bins[key]
			if acc == nil {
				acc = &binAcc{}
				bins[key] = acc
			}
			acc.r += pr
			acc.g += pg
			acc.b += pb
			acc.count++
		}
	}
	var best *binAcc
	for _, acc := range bins {
		if best == nil || acc.count > best.count {
			best = acc
		}
	}
	if best == nil || best.count == 0 {
		return [3]float64{255, 255, 255}, "#FFFFFF", nil
	}
	rgb := [3]float64{best.r / float64(best.count), best.g / float64(best.count), best.b / float64(best.count)}
	hexStr := fmt.Sprintf("#%02X%02X%02X", int(math.Round(rgb[0])), int(math.Round(rgb[1])), int(math.Round(rgb[2])))
	return rgb, hexStr, nil
}

func detectSamenessWarnings(dir string, run evalRun) []string {
	type sample struct {
		id     string
		kind   string
		thumb  *thumbGrid
		rgb    [3]float64
		hexStr string
	}
	var samples []sample
	for _, b := range run.Briefs {
		if b.Kind == "existing" || len(b.Shots) == 0 {
			continue
		}
		shotPath := filepath.Join(dir, b.Shots[0].File)
		th, err1 := computeThumb(shotPath, b.Kind)
		rgb, hexStr, err2 := dominantGroundHex(shotPath, b.Kind)
		if err1 == nil && err2 == nil {
			samples = append(samples, sample{id: b.ID, kind: b.Kind, thumb: th, rgb: rgb, hexStr: hexStr})
		}
	}
	var warnings []string
	for i := 0; i < len(samples); i++ {
		for j := i + 1; j < len(samples); j++ {
			a, b := samples[i], samples[j]
			if a.kind != b.kind {
				continue
			}
			col, lay := thumbSimilarity(a.thumb, b.thumb)
			gDist := math.Hypot(math.Hypot(a.rgb[0]-b.rgb[0], a.rgb[1]-b.rgb[1]), a.rgb[2]-b.rgb[2])
			if col >= sameColourThreshold || lay >= sameLayoutThreshold || gDist <= sameGroundThreshold {
				warnings = append(warnings, fmt.Sprintf("%s/%s (colour=%.2f, layout=%.2f, ground=%s vs %s dist=%.1f)",
					a.id, b.id, col, lay, a.hexStr, b.hexStr, gDist))
			}
		}
	}
	return warnings
}

func buildSummaryMarkdown(dir string, run evalRun) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Design Eval — %s (%s · %s)\n\n", run.Meta.Date, run.Meta.SHA, run.Meta.Backend))
	okCount := 0
	for _, b := range run.Briefs {
		if b.OK {
			okCount++
		}
	}
	sb.WriteString(fmt.Sprintf("- Briefs passed: %d/%d\n", okCount, len(run.Briefs)))
	warnings := detectSamenessWarnings(dir, run)
	if len(warnings) == 0 {
		sb.WriteString("- Sameness flags: none\n\n")
	} else {
		sb.WriteString("- Sameness flags:\n")
		for _, w := range warnings {
			sb.WriteString("  - " + w + "\n")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("| Brief | Kind | OK | Executes | Failed | Turns | Wall (s) |\n")
	sb.WriteString("|---|---|---|---:|---:|---:|---:|\n")
	for _, b := range run.Briefs {
		sb.WriteString(fmt.Sprintf("| %s | %s | %v | %d | %d | %d | %.1f |\n",
			b.ID, b.Kind, b.OK, b.ExecuteCalls, b.FailedExecutes, b.Turns, float64(b.WallMS)/1000.0))
	}
	return sb.String()
}

func cmdCompare(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("compare requires <runA> <runB>")
	}
	dirA, err := filepath.Abs(args[0])
	if err != nil {
		return err
	}
	dirB, err := filepath.Abs(args[1])
	if err != nil {
		return err
	}
	outHTML := filepath.Join(dirB, "compare.html")
	if s, ok, err := flagVal(args[2:], "out"); err != nil {
		return err
	} else if ok {
		outHTML, _ = filepath.Abs(s)
	}

	readRun := func(d string) (evalRun, error) {
		var r evalRun
		raw, err := os.ReadFile(filepath.Join(d, "metrics.json"))
		if err != nil {
			return r, err
		}
		return r, json.Unmarshal(raw, &r)
	}
	runA, err := readRun(dirA)
	if err != nil {
		return err
	}
	runB, err := readRun(dirB)
	if err != nil {
		return err
	}

	warnA := detectSamenessWarnings(dirA, runA)
	warnB := detectSamenessWarnings(dirB, runB)

	var page strings.Builder
	page.WriteString(`<!doctype html><meta charset="utf-8"><title>Design eval comparison</title><style>
body{font:14px/1.45 system-ui,sans-serif;margin:24px;color:#222;background:#fff}
table{border-collapse:collapse;margin-bottom:20px}td,th{border:1px solid #ddd;padding:6px 10px;vertical-align:top;text-align:left}
.side{width:100%;table-layout:fixed}.shots{display:flex;flex-wrap:wrap;gap:8px}.shots img{max-width:100%;max-height:900px;border:1px solid #ccc}
blockquote{margin:0 0 8px;padding-left:10px;border-left:3px solid #ccc;color:#444;white-space:pre-wrap}
</style><h1>Design eval comparison</h1>`)

	page.WriteString(fmt.Sprintf(`<table><tr><th>Metric</th><th>A: %s (%s)</th><th>B: %s (%s)</th></tr>`,
		html.EscapeString(runA.Meta.Date), html.EscapeString(runA.Meta.SHA),
		html.EscapeString(runB.Meta.Date), html.EscapeString(runB.Meta.SHA)))
	sA := "none"
	if len(warnA) > 0 {
		sA = html.EscapeString(strings.Join(warnA, "; "))
	}
	sB := "none"
	if len(warnB) > 0 {
		sB = html.EscapeString(strings.Join(warnB, "; "))
	}
	page.WriteString(fmt.Sprintf(`<tr><td>Looks alike (sameness)</td><td>%s</td><td>%s</td></tr></table>`, sA, sB))

	var allIDs []string
	for _, b := range runA.Briefs {
		allIDs = append(allIDs, b.ID)
	}
	for _, b := range runB.Briefs {
		if !slices.Contains(allIDs, b.ID) {
			allIDs = append(allIDs, b.ID)
		}
	}

	findBrief := func(r evalRun, id string) *evalBriefResult {
		for i := range r.Briefs {
			if r.Briefs[i].ID == id {
				return &r.Briefs[i]
			}
		}
		return nil
	}
	renderCell := func(runDir string, br *evalBriefResult) string {
		if br == nil {
			return "<td>not run</td>"
		}
		var imgs strings.Builder
		for _, sh := range br.Shots {
			rel, err := filepath.Rel(filepath.Dir(outHTML), filepath.Join(runDir, sh.File))
			if err != nil {
				rel = filepath.Join(runDir, sh.File)
			}
			rel = filepath.ToSlash(rel)
			imgs.WriteString(fmt.Sprintf(`<a href="%s"><img src="%s" title="%s"></a>`,
				html.EscapeString(rel), html.EscapeString(rel), html.EscapeString(sh.Name)))
		}
		return fmt.Sprintf(`<td><p>executes %d · failed %d · turns %d · %.1fs</p><blockquote>%s</blockquote><div class="shots">%s</div></td>`,
			br.ExecuteCalls, br.FailedExecutes, br.Turns, float64(br.WallMS)/1000.0,
			html.EscapeString(br.FirstMessage), imgs.String())
	}

	for _, id := range allIDs {
		ba := findBrief(runA, id)
		bb := findBrief(runB, id)
		prompt := ""
		if ba != nil {
			prompt = ba.Brief
		} else if bb != nil {
			prompt = bb.Brief
		}
		page.WriteString(fmt.Sprintf(`<h2>%s</h2><p>%s</p><table class="side"><tr>%s%s</tr></table>`,
			html.EscapeString(id), html.EscapeString(prompt), renderCell(dirA, ba), renderCell(dirB, bb)))
	}

	if err := os.WriteFile(outHTML, []byte(page.String()), 0o644); err != nil {
		return err
	}
	fmt.Printf("Wrote %s\n", outHTML)
	return nil
}
