package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeSkillFixture(t *testing.T, root, folder, content string) string {
	t.Helper()
	dir := filepath.Join(root, folder)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDiscoverExtractAndExpandMultipleSkills(t *testing.T) {
	skillsRoot := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(skillsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkillFixture(t, skillsRoot, "golang-pro", "---\nname: golang-pro\ndescription: Write idiomatic Go code.\n---\nAlways handle errors.")
	writeSkillFixture(t, skillsRoot, "frontend-design", "---\nname: frontend-design\ndescription: High-craft interfaces.\n---\nAvoid generic templates.")

	// Test symlink/NTFS Junction traversal (common in ~/.claude/skills on Windows).
	targetRoot := t.TempDir()
	linkedTarget := writeSkillFixture(t, targetRoot, "junction-target", "---\nname: junction-skill\ndescription: Loaded through link.\n---\nJunction body.")
	junctionPath := filepath.Join(skillsRoot, "junction-skill")
	if runtime.GOOS == "windows" {
		if out, err := exec.Command("cmd", "/C", "mklink", "/J", junctionPath, linkedTarget).CombinedOutput(); err != nil {
			t.Logf("skipping NTFS junction check: %v (%s)", err, string(out))
		}
	} else {
		_ = os.Symlink(linkedTarget, junctionPath)
	}

	t.Setenv("NALARUPA_SKILLS_ONLY", "1")
	t.Setenv("NALARUPA_SKILLS_DIR", skillsRoot)

	catalog := DiscoverSkills("")
	if len(catalog) < 2 {
		t.Fatalf("expected at least 2 skills, got %+v", catalog)
	}
	var names []string
	for _, s := range catalog {
		names = append(names, s.Name)
	}
	if !strings.Contains(strings.Join(names, ","), "golang-pro") || !strings.Contains(strings.Join(names, ","), "frontend-design") {
		t.Fatalf("missing expected skills in %v", names)
	}
	if _, err := os.Stat(junctionPath); err == nil {
		if !strings.Contains(strings.Join(names, ","), "junction-skill") {
			t.Fatalf("expected junction-skill to be discovered across junction, got %v", names)
		}
	}

	// Multiple slash skills + URL ignore + deduplication
	prompt := "/golang-pro Check https://example.com/frontend-design and apply /frontend-design plus /golang-pro /unknown-cmd"
	matched, clean := ExtractSlashSkills(prompt, catalog)
	if len(matched) != 2 || matched[0].Name != "golang-pro" || matched[1].Name != "frontend-design" {
		t.Fatalf("matched = %+v", matched)
	}
	if strings.Contains(clean, "/golang-pro") || !strings.Contains(clean, "https://example.com/frontend-design") || !strings.Contains(clean, "/unknown-cmd") {
		t.Fatalf("unexpected clean prompt: %q", clean)
	}

	expanded := ExpandPromptWithSkills(clean, matched)
	if !strings.Contains(expanded, `<activated_skills>`) ||
		!strings.Contains(expanded, `Always handle errors.`) ||
		!strings.Contains(expanded, `Avoid generic templates.`) {
		t.Fatalf("expanded prompt missing activated skills content:\n%s", expanded)
	}

	// Skill-only prompt gets a helpful directive
	onlyMatched, onlyClean := ExtractSlashSkills("/golang-pro", catalog)
	if len(onlyMatched) != 1 || onlyClean == "" {
		t.Fatalf("skill-only extract = (%+v, %q)", onlyMatched, onlyClean)
	}
}
