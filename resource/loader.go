package resource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// DefaultMaxFileBytes bounds a single Markdown resource.
	DefaultMaxFileBytes = 512 * 1024
)

// Options configures resource discovery.
type Options struct {
	// GlobalDir is the global Iris directory (usually ~/.iris).
	GlobalDir string

	// Workdir is the project root. Project resources live in Workdir/AGENTS.md
	// and Workdir/.iris.
	Workdir string

	// MaxFileBytes bounds a single Markdown resource. Zero uses
	// DefaultMaxFileBytes.
	MaxFileBytes int64
}

// Loader discovers global and project resources.
type Loader struct {
	options Options
}

// NewLoader creates a resource loader.
func NewLoader(options Options) *Loader {
	if options.MaxFileBytes <= 0 {
		options.MaxFileBytes = DefaultMaxFileBytes
	}
	return &Loader{options: options}
}

// Load discovers resources with default options.
func Load(options Options) (*Resources, error) {
	return NewLoader(options).Load()
}

// Load discovers resources and returns warnings for non-fatal problems.
func (l *Loader) Load() (*Resources, error) {
	globalDir, err := absOptional(l.options.GlobalDir)
	if err != nil {
		return nil, fmt.Errorf("resolve global resource directory: %w", err)
	}
	workdir, err := absOptional(l.options.Workdir)
	if err != nil {
		return nil, fmt.Errorf("resolve project resource directory: %w", err)
	}

	projectDir := ""
	if workdir != "" {
		projectDir = filepath.Join(workdir, ".iris")
	}

	resources := &Resources{}

	l.loadInstructions(resources, workdir, projectDir)
	l.loadSystemPrompt(resources, globalDir, projectDir)

	skills := make(map[string]Skill)
	l.loadSkills(resources, skills, globalDir, ScopeGlobal)
	l.loadSkills(resources, skills, projectDir, ScopeProject)
	resources.Skills = sortedSkills(skills)

	prompts := make(map[string]PromptTemplate)
	l.loadPrompts(resources, prompts, globalDir, ScopeGlobal)
	l.loadPrompts(resources, prompts, projectDir, ScopeProject)
	resources.Prompts = sortedPrompts(prompts)

	return resources, nil
}

func (l *Loader) loadInstructions(resources *Resources, workdir, projectDir string) {
	candidates := []struct {
		scope Scope
		path  string
	}{
		{ScopeProject, join(workdir, "AGENTS.md")},
		{ScopeProject, join(projectDir, "AGENTS.md")},
	}

	for _, candidate := range candidates {
		if candidate.path == "" {
			continue
		}
		content, ok := l.readFile(resources, candidate.path, candidate.scope)
		if ok {
			resources.Instructions = append(resources.Instructions, Instruction{
				Scope:   candidate.scope,
				Path:    candidate.path,
				Content: content,
			})
		}
	}
}

func (l *Loader) loadSystemPrompt(resources *Resources, globalDir, projectDir string) {
	globalPath := join(globalDir, "SYSTEM.md")
	if content, ok := l.readFile(resources, globalPath, ScopeGlobal); ok {
		resources.SystemPrompt = content
		resources.SystemPromptSource = File{Scope: ScopeGlobal, Path: globalPath}
	}

	projectPath := join(projectDir, "SYSTEM.md")
	if content, ok := l.readFile(resources, projectPath, ScopeProject); ok {
		resources.SystemPrompt = content
		resources.SystemPromptSource = File{Scope: ScopeProject, Path: projectPath}
	}
}

func (l *Loader) loadSkills(resources *Resources, target map[string]Skill, root string, scope Scope) {
	dir := join(root, "skills")
	if dir == "" {
		return
	}

	scopeSkills := make(map[string]Skill)
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			resources.warn("skill discovery %s: %v", path, walkErr)
			return nil
		}
		if entry.IsDir() {
			if path != dir && isHidden(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !regularMarkdown(path, entry) {
			return nil
		}

		content, ok := l.readFile(resources, path, scope)
		if !ok {
			return nil
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			resources.warn("skill path %s: %v", path, err)
			return nil
		}

		document := parseDocument(content)
		if document.warning != "" {
			resources.warn("%s: %s", path, document.warning)
		}
		name := skillName(relative)
		if document.name != "" {
			name = document.name
		}
		if strings.TrimSpace(name) == "" {
			resources.warn("skill %s has no usable name", path)
			return nil
		}

		if _, exists := scopeSkills[name]; exists {
			resources.warn("duplicate %s skill %q; keeping the first", scope, name)
			return nil
		}
		scopeSkills[name] = Skill{
			Name:        name,
			Description: document.description,
			Scope:       scope,
			Path:        path,
			Content:     document.body,
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		resources.warn("skill discovery %s: %v", dir, err)
	}

	names := make([]string, 0, len(scopeSkills))
	for name := range scopeSkills {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		skill := scopeSkills[name]
		if existing, exists := target[name]; exists {
			resources.warn(
				"project skill %q overrides %s skill at %s",
				name,
				scopeName(existing.Scope),
				existing.Path,
			)
		}
		target[name] = skill
	}
}

func (l *Loader) loadPrompts(resources *Resources, target map[string]PromptTemplate, root string, scope Scope) {
	dir := join(root, "prompts")
	if dir == "" {
		return
	}

	scopePrompts := make(map[string]PromptTemplate)
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			resources.warn("prompt discovery %s: %v", path, walkErr)
			return nil
		}
		if entry.IsDir() {
			if path != dir && isHidden(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !regularMarkdown(path, entry) {
			return nil
		}

		content, ok := l.readFile(resources, path, scope)
		if !ok {
			return nil
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			resources.warn("prompt path %s: %v", path, err)
			return nil
		}

		document := parseDocument(content)
		if document.warning != "" {
			resources.warn("%s: %s", path, document.warning)
		}
		name := strings.TrimSuffix(filepath.ToSlash(relative), filepath.Ext(relative))
		if strings.TrimSpace(name) == "" {
			resources.warn("prompt %s has no usable name", path)
			return nil
		}

		if _, exists := scopePrompts[name]; exists {
			resources.warn("duplicate %s prompt %q; keeping the first", scope, name)
			return nil
		}
		scopePrompts[name] = PromptTemplate{
			Name:        name,
			Description: document.description,
			Scope:       scope,
			Path:        path,
			Content:     document.body,
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		resources.warn("prompt discovery %s: %v", dir, err)
	}

	names := make([]string, 0, len(scopePrompts))
	for name := range scopePrompts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		prompt := scopePrompts[name]
		if existing, exists := target[name]; exists {
			resources.warn(
				"project prompt %q overrides %s prompt at %s",
				name,
				scopeName(existing.Scope),
				existing.Path,
			)
		}
		target[name] = prompt
	}
}

func (l *Loader) readFile(resources *Resources, path string, scope Scope) (string, bool) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false
		}
		resources.warn("read %s %s: %v", scope, path, err)
		return "", false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		resources.warn("skip %s symlink %s", scope, path)
		return "", false
	}
	if !info.Mode().IsRegular() {
		return "", false
	}
	if info.Size() > l.options.MaxFileBytes {
		resources.warn(
			"skip %s %s: %d bytes exceeds limit %d",
			scope,
			path,
			info.Size(),
			l.options.MaxFileBytes,
		)
		return "", false
	}

	content, err := os.ReadFile(path)
	if err != nil {
		resources.warn("read %s %s: %v", scope, path, err)
		return "", false
	}
	return strings.TrimSpace(string(content)), true
}

func join(root, name string) string {
	if root == "" {
		return ""
	}
	return filepath.Join(root, name)
}

func skillName(relative string) string {
	relative = filepath.ToSlash(relative)
	extension := filepath.Ext(relative)
	base := filepath.Base(relative)
	if strings.EqualFold(base, "SKILL.md") {
		dir := filepath.Dir(relative)
		if dir == "." {
			return "SKILL"
		}
		return dir
	}
	return strings.TrimSuffix(relative, extension)
}

func sortedSkills(values map[string]Skill) []Skill {
	skills := make([]Skill, 0, len(values))
	for _, skill := range values {
		skills = append(skills, skill)
	}
	sort.Slice(skills, func(i, j int) bool {
		return skills[i].Name < skills[j].Name
	})
	return skills
}

func sortedPrompts(values map[string]PromptTemplate) []PromptTemplate {
	prompts := make([]PromptTemplate, 0, len(values))
	for _, prompt := range values {
		prompts = append(prompts, prompt)
	}
	sort.Slice(prompts, func(i, j int) bool {
		return prompts[i].Name < prompts[j].Name
	})
	return prompts
}

type document struct {
	name        string
	description string
	body        string
	warning     string
}

func parseDocument(content string) document {
	content = strings.TrimPrefix(content, "\ufeff")
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")
	doc := document{body: strings.TrimSpace(content)}

	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		doc.description = deriveDescription(doc.body)
		return doc
	}

	closing := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			closing = i
			break
		}
	}
	if closing < 0 {
		doc.warning = "unterminated front matter"
		doc.description = deriveDescription(doc.body)
		return doc
	}

	for i := 1; i < closing; i++ {
		key, value, ok := strings.Cut(lines[i], ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch key {
		case "name":
			doc.name = value
		case "description":
			doc.description = truncateDescription(value, 160)
		}
	}

	doc.body = strings.TrimSpace(strings.Join(lines[closing+1:], "\n"))
	if doc.description == "" {
		doc.description = deriveDescription(doc.body)
	}
	return doc
}

func deriveDescription(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.TrimLeft(line, "#")
		return truncateDescription(strings.TrimSpace(line), 160)
	}
	return ""
}
