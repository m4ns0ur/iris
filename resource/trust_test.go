package resource

import (
	"strings"
	"testing"
)

func TestEffectiveTrust(t *testing.T) {
	cases := []struct {
		name  string
		trust Trust
		scope Scope
		want  Trust
	}{
		{name: "global default", scope: ScopeGlobal, want: TrustTrusted},
		{name: "project default", scope: ScopeProject, want: TrustUntrusted},
		{name: "unknown default", scope: "", want: TrustUntrusted},
		{name: "global explicit trusted", trust: TrustTrusted, scope: ScopeGlobal, want: TrustTrusted},
		{name: "global explicit untrusted", trust: TrustUntrusted, scope: ScopeGlobal, want: TrustUntrusted},
		{name: "project cannot be promoted", trust: TrustTrusted, scope: ScopeProject, want: TrustUntrusted},
		{name: "project explicit untrusted", trust: TrustUntrusted, scope: ScopeProject, want: TrustUntrusted},
		{name: "host explicit trusted", trust: TrustTrusted, scope: "", want: TrustTrusted},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := (File{Trust: test.trust, Scope: test.scope}).EffectiveTrust(); got != test.want {
				t.Fatalf("File trust = %q, want %q", got, test.want)
			}
			if got := (Instruction{Trust: test.trust, Scope: test.scope}).EffectiveTrust(); got != test.want {
				t.Fatalf("Instruction trust = %q, want %q", got, test.want)
			}
			if got := (Skill{Trust: test.trust, Scope: test.scope}).EffectiveTrust(); got != test.want {
				t.Fatalf("Skill trust = %q, want %q", got, test.want)
			}
			if got := (PromptTemplate{Trust: test.trust, Scope: test.scope}).EffectiveTrust(); got != test.want {
				t.Fatalf("PromptTemplate trust = %q, want %q", got, test.want)
			}
		})
	}
}

func TestHasUntrustedProjectContent(t *testing.T) {
	if (&Resources{
		SystemPrompt: "trusted host instructions",
		Skills:       []Skill{{Scope: ScopeGlobal, Name: "global"}},
		Prompts:      []PromptTemplate{{Scope: ScopeGlobal, Name: "global"}},
	}).HasUntrustedProjectContent() {
		t.Fatal("global-only resources reported untrusted content")
	}

	if !(&Resources{ProjectSystemPrompt: "project system"}).HasUntrustedProjectContent() {
		t.Fatal("project SYSTEM.md was not reported as untrusted content")
	}
	if !(&Resources{Instructions: []Instruction{{Scope: ScopeProject, Content: "project instructions"}}}).HasUntrustedProjectContent() {
		t.Fatal("project instructions were not reported as untrusted content")
	}
	if !(&Resources{Skills: []Skill{{Scope: ScopeProject, Name: "project"}}}).HasUntrustedProjectContent() {
		t.Fatal("project skill was not reported as untrusted content")
	}
	if !(&Resources{Prompts: []PromptTemplate{{Scope: ScopeProject, Name: "project"}}}).HasUntrustedProjectContent() {
		t.Fatal("project prompt was not reported as untrusted content")
	}
	if (&Resources{Instructions: []Instruction{{Scope: ScopeProject, Content: "  "}}}).HasUntrustedProjectContent() {
		t.Fatal("empty project instructions were reported as untrusted content")
	}
}

func TestSanitizeWarning(t *testing.T) {
	got := sanitizeWarning("line\nline\rinjection\t\x1b[31mred")
	for _, char := range got {
		if char == '\n' || char == '\r' || char == '\t' || char == '\x1b' {
			t.Fatalf("control character survived warning sanitization: %q", got)
		}
	}
	if !strings.Contains(got, "line line injection") {
		t.Fatalf("sanitized warning lost text: %q", got)
	}
}
