package resource

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeResource(t *testing.T, root, relative, content string) string {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
	return path
}

func TestLoaderPrecedenceAndDiscovery(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()

	writeResource(t, global, "SYSTEM.md", "global system instructions")
	writeResource(t, global, "AGENTS.md", "global agents")
	writeResource(t, global, "skills/global/SKILL.md", "---\nname: global-skill\ndescription: global description\n---\nGlobal skill body")
	writeResource(t, global, "skills/shared.md", "global shared skill")
	writeResource(t, global, "prompts/review.md", "Global review: {{input}}")
	writeResource(t, global, "prompts/keep.md", "# Keep\nGlobal prompt")

	writeResource(t, project, "AGENTS.md", "project agents")
	writeResource(t, project, ".iris/AGENTS.md", "project iris agents")
	writeResource(t, project, ".iris/SYSTEM.md", "project system instructions")
	writeResource(t, project, ".iris/skills/coding/SKILL.md", "---\ndescription: coding conventions\n---\n# Coding\n\nUse tabs.")
	writeResource(t, project, ".iris/skills/shared.md", "project shared skill")
	writeResource(t, project, ".iris/prompts/review.md", "Project review: {{input}}")
	writeResource(t, project, ".iris/prompts/deep/debug.md", "Debug {{input}}")
	writeResource(t, project, ".iris/skills/.hidden.md", "hidden")

	loaded, err := Load(Options{GlobalDir: global, Workdir: project})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if loaded.SystemPrompt != "global system instructions" {
		t.Fatalf("trusted system prompt = %q, want the global file", loaded.SystemPrompt)
	}
	if loaded.SystemPromptSource.Scope != ScopeGlobal ||
		loaded.SystemPromptSource.EffectiveTrust() != TrustTrusted ||
		loaded.SystemPromptSource.Path == "" {
		t.Fatalf("trusted system prompt source = %+v", loaded.SystemPromptSource)
	}
	if loaded.ProjectSystemPrompt != "project system instructions" {
		t.Fatalf("project system prompt = %q", loaded.ProjectSystemPrompt)
	}
	if loaded.ProjectSystemPromptSource.Scope != ScopeProject ||
		loaded.ProjectSystemPromptSource.EffectiveTrust() != TrustUntrusted ||
		loaded.ProjectSystemPromptSource.Path == "" {
		t.Fatalf("project system prompt source = %+v", loaded.ProjectSystemPromptSource)
	}

	if len(loaded.Instructions) != 2 {
		t.Fatalf("instructions = %+v, want 2 project instructions", loaded.Instructions)
	}
	for _, instruction := range loaded.Instructions {
		if strings.Contains(instruction.Content, "global agents") {
			t.Fatalf("global AGENTS.md was loaded: %+v", loaded.Instructions)
		}
		if instruction.Scope != ScopeProject || instruction.EffectiveTrust() != TrustUntrusted {
			t.Fatalf("instruction provenance = %+v, want project/untrusted", instruction)
		}
	}
	wantInstructions := []struct {
		scope Scope
		text  string
	}{
		{ScopeProject, "project agents"},
		{ScopeProject, "project iris agents"},
	}
	for i, want := range wantInstructions {
		if loaded.Instructions[i].Scope != want.scope || loaded.Instructions[i].Content != want.text {
			t.Fatalf("instruction %d = %+v, want %+v", i, loaded.Instructions[i], want)
		}
	}

	if names := loaded.SkillNames(); strings.Join(names, ",") != "coding,global-skill,shared" {
		t.Fatalf("skills = %v", names)
	}
	coding, ok := loaded.Skill("coding")
	if !ok || !strings.Contains(coding.Content, "Use tabs") || coding.Description != "coding conventions" {
		t.Fatalf("coding skill = %+v, ok=%v", coding, ok)
	}
	if coding.EffectiveTrust() != TrustUntrusted {
		t.Fatalf("coding skill trust = %q, want untrusted", coding.EffectiveTrust())
	}
	globalSkill, ok := loaded.Skill("global-skill")
	if !ok || globalSkill.Scope != ScopeGlobal || globalSkill.Description != "global description" {
		t.Fatalf("global skill = %+v, ok=%v", globalSkill, ok)
	}
	if globalSkill.EffectiveTrust() != TrustTrusted {
		t.Fatalf("global skill trust = %q, want trusted", globalSkill.EffectiveTrust())
	}
	shared, ok := loaded.Skill("shared")
	if !ok || shared.Scope != ScopeProject || shared.Content != "project shared skill" {
		t.Fatalf("shared skill = %+v, ok=%v", shared, ok)
	}
	if shared.EffectiveTrust() != TrustUntrusted {
		t.Fatalf("shared skill trust = %q, want untrusted", shared.EffectiveTrust())
	}
	if _, ok := loaded.Skill(".hidden"); ok {
		t.Fatal("hidden skill was loaded")
	}

	if names := loaded.PromptNames(); strings.Join(names, ",") != "deep/debug,keep,review" {
		t.Fatalf("prompts = %v", names)
	}
	review, ok := loaded.Prompt("review")
	if !ok || review.Scope != ScopeProject || review.EffectiveTrust() != TrustUntrusted {
		t.Fatalf("review prompt = %+v, ok=%v", review, ok)
	}
	if got := review.Render("this code"); got != "Project review: this code" {
		t.Fatalf("rendered review = %q", got)
	}
	keep, ok := loaded.Prompt("keep")
	if !ok || keep.Description != "Keep" {
		t.Fatalf("keep prompt = %+v, ok=%v", keep, ok)
	}
	if keep.EffectiveTrust() != TrustTrusted {
		t.Fatalf("keep prompt trust = %q, want trusted", keep.EffectiveTrust())
	}

	foundOverride := false
	foundProjectSystem := false
	for _, warning := range loaded.Warnings {
		if strings.Contains(warning, `project skill "shared" overrides`) ||
			strings.Contains(warning, `project prompt "review" overrides`) {
			foundOverride = true
		}
		if strings.Contains(warning, "project SYSTEM.md is untrusted") {
			foundProjectSystem = true
		}
	}
	if !foundOverride || !foundProjectSystem {
		t.Fatalf("expected override and project-system warnings: %v", loaded.Warnings)
	}
	if !loaded.HasUntrustedProjectContent() {
		t.Fatal("project resources were not marked as untrusted")
	}
}

func TestLoaderMissingResourcesAndLimits(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()

	loaded, err := Load(Options{GlobalDir: global, Workdir: project, MaxFileBytes: 64})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.SystemPrompt != "" || len(loaded.Skills) != 0 || len(loaded.Prompts) != 0 {
		t.Fatalf("empty resources = %+v", loaded)
	}

	writeResource(t, project, ".iris/skills/big/SKILL.md", strings.Repeat("x", 128))
	writeResource(t, project, ".iris/prompts/bad.md", "---\ndescription: broken\nno closing")
	writeResource(t, project, ".iris/skills/ok.md", "ok")

	loaded, err = Load(Options{GlobalDir: global, Workdir: project, MaxFileBytes: 64})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, ok := loaded.Skill("ok"); !ok || len(loaded.Skills) != 1 {
		t.Fatalf("skills = %+v", loaded.Skills)
	}
	if len(loaded.Prompts) != 1 {
		t.Fatalf("prompts = %+v", loaded.Prompts)
	}
	warnings := strings.Join(loaded.Warnings, "\n")
	if !strings.Contains(warnings, "exceeds limit") {
		t.Fatalf("size warning missing: %v", loaded.Warnings)
	}
	if !strings.Contains(warnings, "unterminated front matter") {
		t.Fatalf("front matter warning missing: %v", loaded.Warnings)
	}
}

func TestLoaderSkipsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}

	global := t.TempDir()
	project := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(target, []byte("outside"), 0o644); err != nil {
		t.Fatalf("WriteFile(target): %v", err)
	}

	link := filepath.Join(project, ".iris", "skills", "linked.md")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	loaded, err := Load(Options{GlobalDir: global, Workdir: project})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, ok := loaded.Skill("linked"); ok {
		t.Fatal("symlinked skill was loaded")
	}
	if !strings.Contains(strings.Join(loaded.Warnings, "\n"), "symlink") {
		t.Fatalf("symlink warning missing: %v", loaded.Warnings)
	}
}

func TestPromptRenderWithoutPlaceholder(t *testing.T) {
	prompt := PromptTemplate{Content: "Review this code."}
	if got := prompt.Render("focus on errors"); got != "Review this code.\n\nfocus on errors" {
		t.Fatalf("Render() = %q", got)
	}
	if got := prompt.Render(""); got != "Review this code." {
		t.Fatalf("Render(empty) = %q", got)
	}
}

func TestNilResourcesLookups(t *testing.T) {
	var resources *Resources
	if _, ok := resources.Skill("x"); ok {
		t.Fatal("nil resources returned a skill")
	}
	if _, ok := resources.Prompt("x"); ok {
		t.Fatal("nil resources returned a prompt")
	}
	if names := resources.SkillNames(); names != nil {
		t.Fatalf("nil skill names = %v", names)
	}
}

func TestLoaderOnlyLoadsRegularMarkdown(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()

	writeResource(t, project, ".iris/SYSTEM.md", "project system instructions")
	writeResource(t, project, ".iris/skills/evil.sh", "#!/bin/sh\necho pwned")
	writeResource(t, project, ".iris/skills/evil.py", "print('pwned')")
	writeResource(t, project, ".iris/skills/evil.so", "binary")
	writeResource(t, project, ".iris/skills/ok.md", "Use tabs.")
	if runtime.GOOS != "windows" {
		executable := writeResource(t, project, ".iris/skills/executable.md", "Executable Markdown must not load.")
		if err := os.Chmod(executable, 0o755); err != nil {
			t.Fatalf("Chmod() error = %v", err)
		}
	}
	writeResource(t, project, ".iris/prompts/evil.sh", "#!/bin/sh")
	writeResource(t, project, ".iris/prompts/ok.md", "Review: {{input}}")
	writeResource(t, project, ".iris/extensions/evil.md", "extension content")
	writeResource(t, project, ".iris/settings.toml", "security = 'allow'")

	loaded, err := Load(Options{GlobalDir: global, Workdir: project})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.SystemPrompt != "" {
		t.Fatalf("project SYSTEM.md became a trusted system prompt: %q", loaded.SystemPrompt)
	}
	if loaded.ProjectSystemPrompt != "project system instructions" {
		t.Fatalf("project system prompt = %q", loaded.ProjectSystemPrompt)
	}
	if loaded.ProjectSystemPromptSource.EffectiveTrust() != TrustUntrusted {
		t.Fatalf("project system trust = %q, want untrusted", loaded.ProjectSystemPromptSource.EffectiveTrust())
	}
	if names := loaded.SkillNames(); strings.Join(names, ",") != "ok" {
		t.Fatalf("skills = %v, want only regular Markdown", names)
	}
	if names := loaded.PromptNames(); strings.Join(names, ",") != "ok" {
		t.Fatalf("prompts = %v, want only regular Markdown", names)
	}
	if !loaded.HasUntrustedProjectContent() {
		t.Fatal("project content was not marked untrusted")
	}
}

func TestProjectResourcesCannotBePromotedToTrusted(t *testing.T) {
	for _, trust := range []Trust{"", TrustTrusted, TrustUntrusted} {
		instruction := Instruction{Scope: ScopeProject, Trust: trust}
		if got := instruction.EffectiveTrust(); got != TrustUntrusted {
			t.Fatalf("project instruction trust = %q for explicit %q, want untrusted", got, trust)
		}
		skill := Skill{Scope: ScopeProject, Trust: trust}
		if got := skill.EffectiveTrust(); got != TrustUntrusted {
			t.Fatalf("project skill trust = %q for explicit %q, want untrusted", got, trust)
		}
		prompt := PromptTemplate{Scope: ScopeProject, Trust: trust}
		if got := prompt.EffectiveTrust(); got != TrustUntrusted {
			t.Fatalf("project prompt trust = %q for explicit %q, want untrusted", got, trust)
		}
	}
}
