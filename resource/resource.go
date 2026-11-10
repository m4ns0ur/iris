// Package resource loads Iris instruction resources from global and project
// locations.
//
// Discovery and precedence:
//
//   - AGENTS.md is project-specific: it is layered from the project root
//     (<workdir>/AGENTS.md) to the project .iris directory
//     (<workdir>/.iris/AGENTS.md). Both are included; the .iris instructions
//     follow the root instructions. A global AGENTS.md is intentionally not
//     loaded.
//   - SYSTEM.md is a single resource. A project .iris/SYSTEM.md replaces the
//     global ~/.iris/SYSTEM.md.
//   - Skills are Markdown-only. <dir>/skills/name/SKILL.md and
//     <dir>/skills/name.md both produce a skill named "name". Nested Markdown
//     files use their relative path without the .md suffix.
//   - Prompt templates are Markdown-only. <dir>/prompts/name.md produces
//     "name"; nested files use their relative path without the .md suffix.
//   - Project skills and prompt templates with the same name replace global
//     ones. Missing files/directories are not errors.
//
// Skills and prompts may optionally begin with a simple front matter block:
//
//	---
//	name: coding
//	description: Project coding conventions
//	---
//
// The name override applies to skills; prompt names always come from their
// file path. Markdown files larger than the loader limit are skipped with a
// warning. Hidden files and directories are ignored.
package resource

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// Scope identifies where a resource was discovered.
type Scope string

const (
	ScopeGlobal  Scope = "global"
	ScopeProject Scope = "project"
)

// Trust identifies whether content may act as instructions or only as
// untrusted data. Trusted content comes from Iris itself or user-level
// configuration. Project content is always untrusted input.
type Trust string

const (
	TrustTrusted   Trust = "trusted"
	TrustUntrusted Trust = "untrusted"
)

// TrustForScope returns the default trust level for a resource scope.
// Unknown scopes are untrusted.
func TrustForScope(scope Scope) Trust {
	if scope == ScopeGlobal {
		return TrustTrusted
	}
	return TrustUntrusted
}

func resolveTrust(explicit Trust, scope Scope) Trust {
	// Project content is untrusted by construction. A caller cannot promote a
	// project resource to trusted instructions by setting the field.
	if scope == ScopeProject {
		return TrustUntrusted
	}
	if explicit == TrustTrusted || explicit == TrustUntrusted {
		return explicit
	}
	return TrustForScope(scope)
}

// File identifies a discovered resource file.
type File struct {
	Scope Scope
	Path  string
	Trust Trust
}

// EffectiveTrust returns the explicit trust or the scope default.
func (f File) EffectiveTrust() Trust {
	return resolveTrust(f.Trust, f.Scope)
}

// Instruction is a project AGENTS.md layer. Project instructions are untrusted
// input: they can describe conventions, but they cannot change Iris's trusted
// rules, execution policy, or security boundaries.
type Instruction struct {
	Scope   Scope
	Path    string
	Trust   Trust
	Content string
}

// EffectiveTrust returns the explicit trust or the scope default.
func (i Instruction) EffectiveTrust() Trust {
	return resolveTrust(i.Trust, i.Scope)
}

// Skill is a Markdown-only reusable instruction resource. Project skills are
// untrusted data and must not become trusted system instructions.
type Skill struct {
	Name        string
	Description string
	Scope       Scope
	Path        string
	Trust       Trust
	Content     string
}

// EffectiveTrust returns the explicit trust or the scope default.
func (s Skill) EffectiveTrust() Trust {
	return resolveTrust(s.Trust, s.Scope)
}

// PromptTemplate is a reusable user prompt template. Project templates are
// untrusted data and must not become trusted system instructions.
type PromptTemplate struct {
	Name        string
	Description string
	Scope       Scope
	Path        string
	Trust       Trust
	Content     string
}

// EffectiveTrust returns the explicit trust or the scope default.
func (p PromptTemplate) EffectiveTrust() Trust {
	return resolveTrust(p.Trust, p.Scope)
}

// Render substitutes {{input}}. Without a placeholder, non-empty input is
// appended after the template.
func (t PromptTemplate) Render(input string) string {
	input = strings.TrimSpace(input)
	if strings.Contains(t.Content, "{{input}}") {
		return strings.ReplaceAll(t.Content, "{{input}}", input)
	}
	if input == "" {
		return t.Content
	}
	return strings.TrimRight(t.Content, "\n") + "\n\n" + input
}

// Resources is the complete set of discovered instruction resources.
type Resources struct {
	// SystemPrompt is trusted user-level configuration (or programmatically
	// supplied host configuration). It is never replaced by project content.
	SystemPrompt       string
	SystemPromptSource File

	// ProjectSystemPrompt is a project .iris/SYSTEM.md. It is untrusted
	// project context and cannot act as a trusted system prompt.
	ProjectSystemPrompt       string
	ProjectSystemPromptSource File

	Instructions []Instruction
	Skills       []Skill
	Prompts      []PromptTemplate
	Warnings     []string
}

// HasUntrustedProjectContent reports whether any project-level instructions,
// skills, or prompt templates were discovered.
func (r *Resources) HasUntrustedProjectContent() bool {
	if r == nil {
		return false
	}
	if strings.TrimSpace(r.ProjectSystemPrompt) != "" {
		return true
	}
	for _, instruction := range r.Instructions {
		if instruction.EffectiveTrust() == TrustUntrusted && strings.TrimSpace(instruction.Content) != "" {
			return true
		}
	}
	for _, skill := range r.Skills {
		if skill.EffectiveTrust() == TrustUntrusted {
			return true
		}
	}
	for _, prompt := range r.Prompts {
		if prompt.EffectiveTrust() == TrustUntrusted {
			return true
		}
	}
	return false
}

// Skill returns a skill by exact name, then case-insensitively.
func (r *Resources) Skill(name string) (Skill, bool) {
	if r == nil {
		return Skill{}, false
	}
	for _, skill := range r.Skills {
		if skill.Name == name {
			return skill, true
		}
	}
	for _, skill := range r.Skills {
		if strings.EqualFold(skill.Name, name) {
			return skill, true
		}
	}
	return Skill{}, false
}

// Prompt returns a prompt template by exact name, then case-insensitively.
func (r *Resources) Prompt(name string) (PromptTemplate, bool) {
	if r == nil {
		return PromptTemplate{}, false
	}
	for _, prompt := range r.Prompts {
		if prompt.Name == name {
			return prompt, true
		}
	}
	for _, prompt := range r.Prompts {
		if strings.EqualFold(prompt.Name, name) {
			return prompt, true
		}
	}
	return PromptTemplate{}, false
}

// SkillNames returns sorted skill names.
func (r *Resources) SkillNames() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.Skills))
	for _, skill := range r.Skills {
		names = append(names, skill.Name)
	}
	sort.Strings(names)
	return names
}

// PromptNames returns sorted prompt names.
func (r *Resources) PromptNames() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.Prompts))
	for _, prompt := range r.Prompts {
		names = append(names, prompt.Name)
	}
	sort.Strings(names)
	return names
}

func (r *Resources) warn(format string, args ...any) {
	// Warnings are printed to a terminal and written to a log. Resource paths
	// and error text can originate in an untrusted repository, so strip control
	// characters that could inject terminal escapes or forge log lines.
	r.Warnings = append(r.Warnings, sanitizeWarning(fmt.Sprintf(format, args...)))
}

func sanitizeWarning(text string) string {
	var builder strings.Builder
	for _, char := range text {
		switch {
		case char == '\n' || char == '\r' || char == '\t':
			builder.WriteByte(' ')
		case unicode.IsControl(char):
			builder.WriteRune('?')
		default:
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

func scopeName(scope Scope) string {
	if scope == "" {
		return "unknown"
	}
	return string(scope)
}

func absOptional(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return absolute, nil
}

func isHidden(name string) bool {
	return strings.HasPrefix(name, ".")
}

func regularMarkdown(path string, entry os.DirEntry) bool {
	if entry.IsDir() {
		return false
	}
	if isHidden(entry.Name()) {
		return false
	}
	return strings.EqualFold(filepath.Ext(entry.Name()), ".md")
}

func truncateDescription(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= limit {
		return text
	}
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit]) + "..."
	}
	return text
}
