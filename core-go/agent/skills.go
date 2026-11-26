package agent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
)

// SkillInfo describes a locally installed agent skill discovered on disk.
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	Dir         string `json:"dir"`
	Source      string `json:"source"`
}

const (
	maxSkillFileBytes     = 24 * 1024
	maxSkillTotalBytes    = 64 * 1024
	maxSkillMetaReadBytes = 4096
	maxSkillDescRunes     = 180
	skillCacheTTL         = 10 * time.Second
)

var (
	skillCacheMu sync.Mutex
	skillCache   = struct {
		key   string
		at    time.Time
		items []SkillInfo
	}{}
)

// DiscoverSkills scans configured, workspace, and user-level skill directories with a short TTL cache.
// On Windows it inspects entries with os.Stat so NTFS Junctions are followed reliably.
func DiscoverSkills(docPath string) []SkillInfo {
	return DiscoverSkillsRefresh(docPath, false)
}

// DiscoverSkillsRefresh scans configured, workspace, and user-level skill directories, bypassing cache when force is true.
func DiscoverSkillsRefresh(docPath string, force bool) []SkillInfo {
	cacheKey := docPath + "\x00" + os.Getenv("NALARUPA_SKILLS_DIR") + "\x00" + os.Getenv("NALARUPA_SKILLS_ONLY")
	if !force {
		skillCacheMu.Lock()
		if skillCache.key == cacheKey && !skillCache.at.IsZero() && time.Since(skillCache.at) < skillCacheTTL {
			out := append([]SkillInfo(nil), skillCache.items...)
			skillCacheMu.Unlock()
			return out
		}
		skillCacheMu.Unlock()
	}

	type scanRoot struct {
		dir    string
		source string
	}
	var roots []scanRoot
	seenRoots := map[string]bool{}
	addRoot := func(dir, source string) {
		if strings.TrimSpace(dir) == "" {
			return
		}
		clean := filepath.Clean(dir)
		key := clean
		if runtime.GOOS == "windows" {
			key = strings.ToLower(clean)
		}
		if seenRoots[key] {
			return
		}
		seenRoots[key] = true
		roots = append(roots, scanRoot{dir: clean, source: source})
	}

	if envDirs := strings.TrimSpace(os.Getenv("NALARUPA_SKILLS_DIR")); envDirs != "" {
		for _, part := range filepath.SplitList(envDirs) {
			addRoot(part, "env")
		}
	}

	if os.Getenv("NALARUPA_SKILLS_ONLY") != "1" {
		// Workspace / document-relative roots (walk up up to 3 parent levels).
		var anchors []string
		if strings.TrimSpace(docPath) != "" {
			anchors = append(anchors, filepath.Dir(docPath))
		}
		if wd, err := os.Getwd(); err == nil && wd != "" {
			anchors = append(anchors, wd)
		}
		for _, start := range anchors {
			cur := filepath.Clean(start)
			for lvl := 0; lvl < 4 && cur != "" && cur != string(filepath.Separator); lvl++ {
				for _, rel := range []string{
					filepath.Join(cur, ".agents", "skills"),
					filepath.Join(cur, ".claude", "skills"),
					filepath.Join(cur, ".cursor", "skills"),
					filepath.Join(cur, ".gemini", "skills"),
					filepath.Join(cur, "skills"),
				} {
					addRoot(rel, "workspace")
				}
				parent := filepath.Dir(cur)
				if parent == cur {
					break
				}
				cur = parent
			}
		}

		// User home global agent skill directories.
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			userRoots := []scanRoot{
				{filepath.Join(home, ".agents", "skills"), ".agents"},
				{filepath.Join(home, ".claude", "skills"), ".claude"},
				{filepath.Join(home, ".cursor", "skills"), ".cursor"},
				{filepath.Join(home, ".codex", "skills"), ".codex"},
				{filepath.Join(home, ".gemini", "config", "skills"), ".gemini"},
				{filepath.Join(home, ".gemini", "antigravity", "skills"), "antigravity"},
				{filepath.Join(home, ".gemini", "antigravity", "builtin", "skills"), "antigravity"},
			}
			for _, ur := range userRoots {
				addRoot(ur.dir, ur.source)
			}
		}
	}

	seenNames := map[string]bool{}
	var out []SkillInfo
	for _, root := range roots {
		entries, err := os.ReadDir(root.dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			entryDir := filepath.Join(root.dir, entry.Name())
			// os.Stat dereferences NTFS Junctions & symlinks where DirEntry.IsDir() returns false on Windows.
			fi, err := os.Stat(entryDir)
			if err != nil || !fi.IsDir() {
				continue
			}
			skillFile := findSkillMarkdown(entryDir)
			if skillFile == "" {
				continue
			}
			name, desc := parseSkillMetadata(skillFile, entry.Name())
			slug := strings.ToLower(strings.TrimSpace(name))
			if slug == "" || seenNames[slug] {
				continue
			}
			seenNames[slug] = true
			out = append(out, SkillInfo{
				Name:        name,
				Description: desc,
				Path:        skillFile,
				Dir:         entryDir,
				Source:      root.source,
			})
		}
	}

	slices.SortFunc(out, func(a, b SkillInfo) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})

	skillCacheMu.Lock()
	skillCache.key = cacheKey
	skillCache.at = time.Now()
	skillCache.items = append([]SkillInfo(nil), out...)
	skillCacheMu.Unlock()

	return out
}

func findSkillMarkdown(dir string) string {
	for _, candidate := range []string{"SKILL.md", "skill.md"} {
		p := filepath.Join(dir, candidate)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

func parseSkillMetadata(skillFile, fallbackName string) (name, desc string) {
	f, err := os.Open(skillFile)
	if err != nil {
		return fallbackName, ""
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxSkillMetaReadBytes))
	if err != nil {
		return fallbackName, ""
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.Split(text, "\n")

	bodyStart := 0
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for i := 1; i < len(lines); i++ {
			trimmed := strings.TrimSpace(lines[i])
			if trimmed == "---" {
				bodyStart = i + 1
				break
			}
			if key, val, ok := strings.Cut(trimmed, ":"); ok {
				k := strings.ToLower(strings.TrimSpace(key))
				v := strings.TrimSpace(val)
				v = strings.Trim(v, `"'`)
				switch k {
				case "name":
					if v != "" {
						name = v
					}
				case "description":
					if v == "|" || v == ">" || v == "|-" || v == ">-" {
						var multi []string
						for j := i + 1; j < len(lines); j++ {
							if strings.TrimSpace(lines[j]) == "---" {
								break
							}
							if len(lines[j]) > 0 && (lines[j][0] == ' ' || lines[j][0] == '\t') {
								multi = append(multi, strings.TrimSpace(lines[j]))
								i = j
							} else if strings.TrimSpace(lines[j]) == "" && len(multi) > 0 {
								i = j
							} else {
								break
							}
						}
						desc = strings.Join(multi, " ")
					} else if v != "" {
						desc = v
					}
				}
			}
		}
	}
	if name == "" {
		name = fallbackName
	}
	if desc == "" {
		for i := bodyStart; i < len(lines); i++ {
			l := strings.TrimSpace(lines[i])
			if l == "" || strings.HasPrefix(l, "#") || strings.HasPrefix(l, "```") || l == "---" {
				continue
			}
			desc = l
			break
		}
	}
	return name, truncateRunes(desc, maxSkillDescRunes)
}

func truncateRunes(s string, maxR int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= maxR {
		return s
	}
	return strings.TrimSpace(string(r[:maxR-1])) + "…"
}

func isSkillTokenChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '-' || b == '_' || b == '.'
}

func isAllowedBeforeSlash(r rune) bool {
	return unicode.IsSpace(r) || r == '(' || r == '[' || r == '{' || r == ',' || r == ';' || r == '"' || r == '\''
}

// ExtractSlashSkills finds `/skill-name` mentions in prompt against catalog (supporting multiple skills),
// ignores URLs/paths, deduplicates in mention order, and returns matched skills plus the cleaned prompt.
func ExtractSlashSkills(prompt string, catalog []SkillInfo) (matched []SkillInfo, cleanPrompt string) {
	if len(catalog) == 0 || !strings.Contains(prompt, "/") {
		return nil, strings.TrimSpace(prompt)
	}
	byName := make(map[string]SkillInfo, len(catalog)*2)
	for _, s := range catalog {
		k := strings.ToLower(strings.TrimSpace(s.Name))
		if k != "" {
			if _, exists := byName[k]; !exists {
				byName[k] = s
			}
		}
	}
	for _, s := range catalog {
		bk := strings.ToLower(filepath.Base(s.Dir))
		if bk != "" {
			if _, exists := byName[bk]; !exists {
				byName[bk] = s
			}
		}
	}

	type span struct{ start, end int }
	var remove []span
	seen := map[string]bool{}

	for i := 0; i < len(prompt); i++ {
		if prompt[i] != '/' {
			continue
		}
		if i > 0 {
			prevRune := rune(prompt[i-1])
			if prompt[i-1] >= 0x80 {
				head := prompt[:i]
				for _, r := range head {
					prevRune = r
				}
			}
			if !isAllowedBeforeSlash(prevRune) {
				continue
			}
		}
		end := i + 1
		if end >= len(prompt) || !isSkillTokenChar(prompt[end]) {
			continue
		}
		for end < len(prompt) && isSkillTokenChar(prompt[end]) {
			end++
		}
		// Reject path continuations like `/assets/logo.svg` or `/docs\file.md` so filesystem paths stay untouched.
		if end < len(prompt) && (prompt[end] == '/' || prompt[end] == '\\') {
			i = end
			continue
		}
		token := prompt[i+1 : end]
		skill, ok := byName[strings.ToLower(token)]
		if !ok {
			continue
		}
		remove = append(remove, span{start: i, end: end})
		key := strings.ToLower(skill.Name)
		if !seen[key] {
			seen[key] = true
			matched = append(matched, skill)
		}
		i = end - 1
	}

	if len(matched) == 0 {
		return nil, strings.TrimSpace(prompt)
	}

	var b strings.Builder
	cur := 0
	for _, sp := range remove {
		if sp.start > cur {
			b.WriteString(prompt[cur:sp.start])
		}
		cur = sp.end
		if cur < len(prompt) && prompt[cur] == ' ' {
			cur++
		}
	}
	if cur < len(prompt) {
		b.WriteString(prompt[cur:])
	}
	clean := strings.TrimSpace(b.String())
	if clean == "" {
		names := make([]string, len(matched))
		for i, s := range matched {
			names[i] = "/" + s.Name
		}
		clean = "Apply the activated skill instructions (" + strings.Join(names, ", ") + ") to this task."
	}
	return matched, clean
}

// ExpandPromptWithSkills injects matched SKILL.md bodies once into the prompt sent to any agent backend.
func ExpandPromptWithSkills(prompt string, matched []SkillInfo) string {
	if len(matched) == 0 {
		return prompt
	}
	var sb strings.Builder
	sb.WriteString("Use the following activated local agent skills carefully:\n<activated_skills>\n")
	total := 0
	for _, s := range matched {
		if total >= maxSkillTotalBytes {
			break
		}
		rem := maxSkillTotalBytes - total
		limit := min(maxSkillFileBytes, rem)
		raw, truncatedByRead, err := readBoundedSkillFile(s.Path, limit)
		if err != nil {
			fmt.Fprintf(&sb, "<skill name=%q path=%q status=\"unreadable\">\nSkill file could not be read (%v). Ask the user to reload skills and retry if these instructions are required.\n</skill>\n", s.Name, s.Path, err)
			continue
		}
		body := strings.TrimSpace(strings.ReplaceAll(string(raw), "\r\n", "\n"))
		if len(body) > limit || truncatedByRead {
			if len(body) > limit {
				body = body[:limit]
			}
			body = strings.ToValidUTF8(body, "") + "\n...(truncated)"
		}
		total += len(body)
		fmt.Fprintf(&sb, "<skill name=%q path=%q>\n%s\n</skill>\n", s.Name, s.Path, body)
	}
	sb.WriteString("</activated_skills>\n\n")
	sb.WriteString(prompt)
	return sb.String()
}

func readBoundedSkillFile(path string, maxBytes int) ([]byte, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	// Allow slight slack for CRLF -> LF normalization while strictly bounding memory to maxBytes + 2048.
	capRead := int64(maxBytes) + 2048
	buf, err := io.ReadAll(io.LimitReader(f, capRead+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(buf)) > capRead {
		return buf[:capRead], true, nil
	}
	return buf, false, nil
}

func claudeProjectRootForSkillDir(dir string) string {
	clean := filepath.Clean(dir)
	parent := filepath.Dir(clean)
	if !strings.EqualFold(filepath.Base(parent), "skills") {
		return ""
	}
	dotClaude := filepath.Dir(parent)
	if !strings.EqualFold(filepath.Base(dotClaude), ".claude") {
		return ""
	}
	root := filepath.Dir(dotClaude)
	if root == "" || root == dotClaude {
		return ""
	}
	return root
}

// SkillDirs returns deduplicated, resolved skill directories for Claude CLI --add-dir flags (including the
// enclosing project root when a skill lives inside a `.claude/skills/<name>` layout, plus the skill folder itself).
func SkillDirs(matched []SkillInfo) []string {
	if len(matched) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(d string) {
		if d == "" {
			return
		}
		clean := filepath.Clean(d)
		key := clean
		if runtime.GOOS == "windows" {
			key = strings.ToLower(clean)
		}
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, clean)
	}
	for _, s := range matched {
		if root := claudeProjectRootForSkillDir(s.Dir); root != "" {
			add(root)
		}
		add(s.Dir)
		if resolved, err := filepath.EvalSymlinks(s.Dir); err == nil && resolved != "" {
			if root := claudeProjectRootForSkillDir(resolved); root != "" {
				add(root)
			}
			add(resolved)
		}
	}
	return out
}
