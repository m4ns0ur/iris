package prompt

import (
	"strings"
	"testing"

	"github.com/m4ns0ur/iris/internal/tool"
	"github.com/m4ns0ur/iris/resource"
)

func TestBuildSystemPromptWithoutResources(t *testing.T) {
	prompt := BuildSystemPrompt(&tool.ToolContext{Workdir: "/work"}, nil)
	for _, want := range []string{
		"You are Iris",
		"Working Directory: /work",
		"The user may send new instructions while you are working",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
	if strings.Contains(prompt, "Custom system instructions") ||
		strings.Contains(prompt, "Instruction resources") {
		t.Fatalf("unexpected resource sections:\n%s", prompt)
	}
}

func TestBuildSystemPromptWithResources(t *testing.T) {
	resources := &resource.Resources{
		SystemPrompt: "Always prefer small changes.",
		Instructions: []resource.Instruction{
			{Scope: resource.ScopeGlobal, Content: "Use Go conventions."},
			{Scope: resource.ScopeProject, Content: "Run tests after editing."},
		},
	}

	prompt := BuildSystemPrompt(&tool.ToolContext{Workdir: "/work"}, resources)
	for _, want := range []string{
		"Custom system instructions:\nAlways prefer small changes.",
		"Instruction resources (AGENTS.md):",
		"Use Go conventions.",
		"Run tests after editing.",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}

	if strings.Index(prompt, "Use Go conventions.") > strings.Index(prompt, "Run tests after editing.") {
		t.Fatalf("global instructions should precede project instructions:\n%s", prompt)
	}
}
