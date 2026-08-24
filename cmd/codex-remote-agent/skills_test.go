package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanSkillRootReadsFrontmatter(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "sample-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("create skill directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: sample-skill\ndescription: Sample description\n---\n# Skill\n"), 0o644); err != nil {
		t.Fatalf("write skill metadata: %v", err)
	}

	seen := map[string]SkillOption{}
	scanSkillRoot(root, "skill", seen)
	got, ok := seen["/sample-skill"]
	if !ok || got.Description != "Sample description" || got.Kind != "skill" {
		t.Fatalf("unexpected skill option: %#v", seen)
	}
}

func TestScanPluginRootReadsManifestAndSkills(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "example-plugin", "1.0.0")
	if err := os.MkdirAll(filepath.Join(pluginDir, ".codex-plugin"), 0o755); err != nil {
		t.Fatalf("create plugin directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, ".codex-plugin", "plugin.json"), []byte(`{"name":"example-plugin","description":"Example plugin"}`), 0o644); err != nil {
		t.Fatalf("write plugin metadata: %v", err)
	}
	skillDir := filepath.Join(pluginDir, "skills", "plugin-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("create plugin skill directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\ndescription: Plugin skill\n---\n"), 0o644); err != nil {
		t.Fatalf("write plugin skill metadata: %v", err)
	}

	seen := map[string]SkillOption{}
	scanPluginRoot(root, seen)
	if got := seen["/example-plugin"]; got.Kind != "plugin" || got.Description != "Example plugin" {
		t.Fatalf("unexpected plugin option: %#v", got)
	}
	if got := seen["/plugin-skill"]; got.Kind != "skill" || got.Description != "Plugin skill" {
		t.Fatalf("unexpected plugin skill option: %#v", got)
	}
}
