package policy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newShellPolicy(t *testing.T, options Options) *Policy {
	t.Helper()
	return newTestPolicy(t, options)
}

func TestAnalyzeCommand(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		programs []string
		opaque   bool
	}{
		{
			name:     "quoted separator is not a boundary",
			command:  `echo "a;b"`,
			programs: []string{"echo"},
		},
		{
			name:     "pipeline",
			command:  "echo hi | grep h",
			programs: []string{"echo", "grep"},
		},
		{
			name:     "assignment prefix",
			command:  "FOO=bar ls -la",
			programs: []string{"ls"},
		},
		{
			name:     "redirection target is not a program",
			command:  "echo hi > out.txt && cat out.txt",
			programs: []string{"echo", "cat"},
		},
		{
			name:     "file descriptor redirection",
			command:  "make 2>&1 | tail",
			programs: []string{"make", "tail"},
			opaque:   true, // make can execute arbitrary commands
		},
		{
			name:     "command substitution is opaque",
			command:  "echo $(whoami)",
			programs: []string{"echo"},
			opaque:   true,
		},
		{
			name:     "backticks are opaque",
			command:  "echo `whoami`",
			programs: []string{"echo"},
			opaque:   true,
		},
		{
			name:     "shell wrapper is opaque",
			command:  "bash -c 'ls'",
			programs: []string{"bash"},
			opaque:   true,
		},
		{
			name:     "find exec is opaque",
			command:  "find . -exec rm {} ;",
			programs: []string{"find"},
			opaque:   true,
		},
		{
			name:     "plain find is not opaque",
			command:  "find . -name '*.go'",
			programs: []string{"find"},
		},
		{
			name:     "heredoc is opaque",
			command:  "cat <<EOF",
			programs: []string{"cat"},
			opaque:   true,
		},
		{
			name:     "unterminated quote is opaque",
			command:  `echo "unterminated`,
			programs: []string{"echo"},
			opaque:   true,
		},
		{
			name:     "single quoted substitution is literal",
			command:  `echo '$(whoami)'`,
			programs: []string{"echo"},
		},
		{
			name:     "redirect before command",
			command:  ">out.txt echo hi",
			programs: []string{"echo"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analysis := AnalyzeCommand(test.command)

			if analysis.Opaque != test.opaque {
				t.Fatalf("Opaque = %v, want %v (reason %q)", analysis.Opaque, test.opaque, analysis.OpaqueReason)
			}

			if strings.Join(analysis.Programs, ",") != strings.Join(test.programs, ",") {
				t.Fatalf("Programs = %v, want %v", analysis.Programs, test.programs)
			}
		})
	}
}

func TestAuthorizeShellFailsClosedWithoutApprover(t *testing.T) {
	pol := newShellPolicy(t, Options{})

	_, err := pol.AuthorizeShell(context.Background(), "echo hi", pol.Workdir())
	if !errors.Is(err, ErrApprovalUnavailable) {
		t.Fatalf("AuthorizeShell() error = %v, want ErrApprovalUnavailable", err)
	}

	if !strings.Contains(err.Error(), "approval_hook") {
		t.Fatalf("error should explain how to configure approval: %v", err)
	}
}

func TestAuthorizeShellApproverAllows(t *testing.T) {
	var got ApprovalRequest

	pol := newShellPolicy(t, Options{
		Approver: ApproverFunc(func(_ context.Context, request ApprovalRequest) (ApprovalDecision, error) {
			got = request
			return ApprovalDecision{Allow: true}, nil
		}),
	})

	if _, err := pol.AuthorizeShell(context.Background(), "echo hi", pol.Workdir()); err != nil {
		t.Fatalf("AuthorizeShell() error = %v", err)
	}

	if got.Command != "echo hi" {
		t.Fatalf("approval command = %q, want %q", got.Command, "echo hi")
	}
	if got.Kind != "shell" {
		t.Fatalf("approval kind = %q, want shell", got.Kind)
	}
	if got.Workdir != pol.Workdir() {
		t.Fatalf("approval workdir = %q, want %q", got.Workdir, pol.Workdir())
	}
}

func TestAuthorizeShellApproverDenies(t *testing.T) {
	pol := newShellPolicy(t, Options{
		Approver: ApproverFunc(func(context.Context, ApprovalRequest) (ApprovalDecision, error) {
			return ApprovalDecision{Allow: false, Reason: "not today"}, nil
		}),
	})

	_, err := pol.AuthorizeShell(context.Background(), "echo hi", pol.Workdir())
	if !errors.Is(err, ErrCommandDenied) {
		t.Fatalf("AuthorizeShell() error = %v, want ErrCommandDenied", err)
	}
	if !strings.Contains(err.Error(), "not today") {
		t.Fatalf("error should include approver reason: %v", err)
	}
}

func TestAuthorizeShellApprovalTimesOut(t *testing.T) {
	pol := newShellPolicy(t, Options{
		Shell: ShellOptions{
			Mode:            ShellModeApproval,
			ApprovalTimeout: 50 * time.Millisecond,
		},
		Approver: ApproverFunc(func(ctx context.Context, _ ApprovalRequest) (ApprovalDecision, error) {
			<-ctx.Done()
			return ApprovalDecision{}, ctx.Err()
		}),
	})

	start := time.Now()
	_, err := pol.AuthorizeShell(context.Background(), "echo hi", pol.Workdir())
	elapsed := time.Since(start)

	if !errors.Is(err, ErrApprovalTimeout) {
		t.Fatalf("AuthorizeShell() error = %v, want ErrApprovalTimeout", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("approval timeout took too long: %s", elapsed)
	}
}

func TestAllowlistMode(t *testing.T) {
	pol := newShellPolicy(t, Options{
		Shell: ShellOptions{
			Mode:  ShellModeAllowlist,
			Allow: []string{"ls", "git status*"},
		},
	})

	allowed := []string{"ls", "ls -la", "git status", "git status --short"}
	for _, command := range allowed {
		if _, err := pol.AuthorizeShell(context.Background(), command, pol.Workdir()); err != nil {
			t.Errorf("AuthorizeShell(%q) error = %v, want allowed", command, err)
		}
	}

	denied := []string{
		"cat file.txt",
		"ls && cat file.txt",
		"git status; rm -rf /",
	}
	for _, command := range denied {
		if _, err := pol.AuthorizeShell(context.Background(), command, pol.Workdir()); !errors.Is(err, ErrCommandDenied) {
			t.Errorf("AuthorizeShell(%q) error = %v, want ErrCommandDenied", command, err)
		}
	}
}

func TestAllowlistNeverAllowsOpaqueCommands(t *testing.T) {
	pol := newShellPolicy(t, Options{
		Shell: ShellOptions{
			Mode:  ShellModeAllowlist,
			Allow: []string{"echo"},
		},
	})

	for _, command := range []string{"echo $(id)", "echo `id`", "bash -c 'echo hi'"} {
		_, err := pol.AuthorizeShell(context.Background(), command, pol.Workdir())
		if !errors.Is(err, ErrCommandDenied) {
			t.Errorf("AuthorizeShell(%q) error = %v, want ErrCommandDenied", command, err)
		}
	}
}

func TestDenyRulesWinInAllowMode(t *testing.T) {
	pol := newShellPolicy(t, Options{
		ExternalSandbox: true,
		Shell: ShellOptions{
			Mode:  ShellModeAllow,
			Allow: []string{"*"},
		},
	})

	if _, err := pol.AuthorizeShell(context.Background(), "echo hi", pol.Workdir()); err != nil {
		t.Fatalf("AuthorizeShell() error = %v, want allowed", err)
	}

	for _, command := range []string{"sudo ls", "/usr/bin/sudo ls", "rm -rf /", "/bin/rm -rf /", ":(){ :|:& };:"} {
		if _, err := pol.AuthorizeShell(context.Background(), command, pol.Workdir()); !errors.Is(err, ErrCommandDenied) {
			t.Errorf("AuthorizeShell(%q) error = %v, want ErrCommandDenied", command, err)
		}
	}
}

func TestDisableDefaultDeny(t *testing.T) {
	pol := newShellPolicy(t, Options{
		ExternalSandbox: true,
		Shell: ShellOptions{
			Mode:               ShellModeAllow,
			DisableDefaultDeny: true,
		},
	})

	if _, err := pol.AuthorizeShell(context.Background(), "sudo ls", pol.Workdir()); err != nil {
		t.Fatalf("AuthorizeShell() error = %v, want allowed with defaults disabled", err)
	}
}

func TestShellModeAllowRequiresExternalSandbox(t *testing.T) {
	_, err := New(t.TempDir(), Options{
		Shell: ShellOptions{Mode: ShellModeAllow},
	})
	if !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("New() error = %v, want ErrInvalidPolicy", err)
	}
	if !strings.Contains(err.Error(), "external_sandbox") {
		t.Fatalf("error should mention external_sandbox: %v", err)
	}
}

func TestReadOnlyModeDisablesShell(t *testing.T) {
	pol := newShellPolicy(t, Options{
		Mode:            ModeReadOnly,
		ExternalSandbox: true,
		Shell:           ShellOptions{Mode: ShellModeAllow},
	})

	_, err := pol.AuthorizeShell(context.Background(), "echo hi", pol.Workdir())
	if !errors.Is(err, ErrShellDisabled) {
		t.Fatalf("AuthorizeShell() error = %v, want ErrShellDisabled", err)
	}
}

func TestDenyShellMode(t *testing.T) {
	pol := newShellPolicy(t, Options{
		Shell: ShellOptions{Mode: ShellModeDeny},
	})

	_, err := pol.AuthorizeShell(context.Background(), "echo hi", pol.Workdir())
	if !errors.Is(err, ErrShellDisabled) {
		t.Fatalf("AuthorizeShell() error = %v, want ErrShellDisabled", err)
	}
}

func TestHookApproverAllowAndDeny(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script hooks are not portable to Windows")
	}

	scripts := map[string]string{
		"allow": "#!/bin/sh\ncat >/dev/null\nexit 0\n",
		"deny":  "#!/bin/sh\ncat >/dev/null\necho 'blocked by hook'\nexit 1\n",
	}

	for name, content := range scripts {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hook.sh")
			if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			approver := &HookApprover{
				Command: []string{path},
				Timeout: 2 * time.Second,
			}

			decision, err := approver.Approve(context.Background(), ApprovalRequest{
				Kind:    "shell",
				Command: "echo hi",
			})
			if err != nil {
				t.Fatalf("Approve() error = %v", err)
			}

			wantAllow := name == "allow"
			if decision.Allow != wantAllow {
				t.Fatalf("Allow = %v, want %v (reason %q)", decision.Allow, wantAllow, decision.Reason)
			}
			if name == "deny" && !strings.Contains(decision.Reason, "blocked") {
				t.Fatalf("reason = %q, want hook output", decision.Reason)
			}
		})
	}
}

func TestHookApproverTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script hooks are not portable to Windows")
	}

	path := filepath.Join(t.TempDir(), "hook.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 5\n"), 0o700); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	approver := &HookApprover{
		Command: []string{path},
		Timeout: 100 * time.Millisecond,
	}

	start := time.Now()
	_, err := approver.Approve(context.Background(), ApprovalRequest{Kind: "shell", Command: "echo hi"})
	elapsed := time.Since(start)

	if !errors.Is(err, ErrApprovalTimeout) {
		t.Fatalf("Approve() error = %v, want ErrApprovalTimeout", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("hook timeout took too long: %s", elapsed)
	}
}

func TestPolicyUsesConfiguredApprovalHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script hooks are not portable to Windows")
	}

	path := filepath.Join(t.TempDir(), "hook.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\ncat >/dev/null\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	pol := newShellPolicy(t, Options{
		Shell: ShellOptions{
			Mode:         ShellModeApproval,
			ApprovalHook: []string{path},
		},
	})

	if pol.Approver() == nil {
		t.Fatal("policy should install the configured approval hook")
	}

	if _, err := pol.AuthorizeShell(context.Background(), "echo hi", pol.Workdir()); err != nil {
		t.Fatalf("AuthorizeShell() error = %v", err)
	}
}

func TestRejectsUnboundedOutputConfig(t *testing.T) {
	_, err := New(t.TempDir(), Options{
		Shell: ShellOptions{MaxOutputBytes: 5 << 20},
	})
	if !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("New() error = %v, want ErrInvalidPolicy", err)
	}
}

func TestWarningsMakeSandboxRequirementExplicit(t *testing.T) {
	pol := newShellPolicy(t, Options{})

	warnings := strings.Join(pol.Warnings(), "\n")
	if !strings.Contains(warnings, "not OS-sandboxed") {
		t.Fatalf("warnings = %q, want unsandboxed shell warning", warnings)
	}
	if !strings.Contains(warnings, "container") {
		t.Fatalf("warnings = %q, want container guidance", warnings)
	}
}
