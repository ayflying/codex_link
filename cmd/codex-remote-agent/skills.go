package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type SkillOption struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Command     string `json:"command"`
	Kind        string `json:"kind"`
}

type pluginManifest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func discoverSkillOptions() []SkillOption {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	seen := map[string]SkillOption{}
	for _, root := range []string{
		filepath.Join(codexHome, "skills"),
		filepath.Join(home, ".agents", "skills"),
	} {
		scanSkillRoot(root, "skill", seen)
	}
	for _, root := range []string{
		filepath.Join(codexHome, "plugins", "cache"),
		filepath.Join(codexHome, "plugins", "marketplaces"),
	} {
		scanPluginRoot(root, seen)
	}
	options := make([]SkillOption, 0, len(seen))
	for _, option := range seen {
		options = append(options, option)
	}
	sort.SliceStable(options, func(i, j int) bool {
		if options[i].Kind != options[j].Kind {
			return options[i].Kind < options[j].Kind
		}
		return strings.ToLower(options[i].Name) < strings.ToLower(options[j].Name)
	})
	return options
}

func scanSkillRoot(root, kind string, seen map[string]SkillOption) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		description := readSkillDescription(filepath.Join(root, name, "SKILL.md"))
		if description == "" {
			continue
		}
		addSkillOption(seen, SkillOption{Name: name, Description: description, Command: "/" + name, Kind: kind})
	}
}

func scanPluginRoot(root string, seen map[string]SkillOption) {
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "node_modules", ".git":
				return filepath.SkipDir
			case "skills":
				scanSkillRoot(path, "skill", seen)
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != "plugin.json" || filepath.Base(filepath.Dir(path)) != ".codex-plugin" {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		var manifest pluginManifest
		if json.Unmarshal(raw, &manifest) != nil {
			return nil
		}
		name := strings.TrimSpace(manifest.Name)
		if name == "" {
			return nil
		}
		addSkillOption(seen, SkillOption{
			Name:        name,
			Description: strings.TrimSpace(manifest.Description),
			Command:     "/" + name,
			Kind:        "plugin",
		})
		return nil
	})
}

func addSkillOption(seen map[string]SkillOption, option SkillOption) {
	key := strings.ToLower(strings.TrimSpace(option.Command))
	if key == "" {
		return
	}
	if current, ok := seen[key]; ok {
		if current.Kind == "plugin" && option.Kind == "skill" {
			seen[key] = option
		}
		return
	}
	seen[key] = option
}

func readSkillDescription(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	text := string(raw)
	if strings.HasPrefix(text, "---") {
		inFrontmatter := false
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "---" {
				if inFrontmatter {
					break
				}
				inFrontmatter = true
				continue
			}
			if !inFrontmatter {
				continue
			}
			if strings.HasPrefix(line, "description:") {
				return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "description:")), "\"'")
			}
		}
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			return line
		}
	}
	return ""
}
