package config

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/m4ns0ur/iris/internal/policy"
	"github.com/spf13/viper"
)

func TestLoadSecuritySection(t *testing.T) {
	viper.Reset()
	defer viper.Reset()

	viper.SetConfigType("toml")
	if err := viper.ReadConfig(strings.NewReader(`
[security]
mode = "read-only"
external_sandbox = true
read_paths = ["/tmp/read"]
write_paths = ["/tmp/write"]

[security.shell]
mode = "allowlist"
allow = ["ls", "git status*"]
deny = ["curl*"]
timeout_seconds = 5
approval_timeout_seconds = 7
max_output_bytes = 2048
max_output_lines = 10
max_capture_bytes = 4096
temp_file_ttl_minutes = 30
binary = "/bin/sh"

[llm]
request_timeout_seconds = 42

[agent]
max_turns = 7
max_tool_calls = 11
tool_timeout_seconds = 13
`)); err != nil {
		t.Fatalf("ReadConfig() error = %v", err)
	}

	if err := Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	cfg := GetConfig()
	if cfg.Security.Mode != "read-only" {
		t.Fatalf("mode = %q, want read-only", cfg.Security.Mode)
	}
	if !cfg.Security.ExternalSandbox {
		t.Fatal("external_sandbox = false, want true")
	}

	options := cfg.PolicyOptions()
	if options.Shell.Timeout != 5*time.Second {
		t.Fatalf("timeout = %v, want 5s", options.Shell.Timeout)
	}
	if options.Shell.ApprovalTimeout != 7*time.Second {
		t.Fatalf("approval timeout = %v, want 7s", options.Shell.ApprovalTimeout)
	}
	if options.Shell.MaxOutputBytes != 2048 {
		t.Fatalf("max output bytes = %d, want 2048", options.Shell.MaxOutputBytes)
	}
	if options.Shell.TempDirTTL != 30*time.Minute {
		t.Fatalf("temp ttl = %v, want 30m", options.Shell.TempDirTTL)
	}
	if len(options.Shell.Allow) != 2 || options.Shell.Allow[0] != "ls" {
		t.Fatalf("allow = %v, want [ls git status*]", options.Shell.Allow)
	}
	if options.Shell.ShellBinary != "/bin/sh" {
		t.Fatalf("binary = %q, want /bin/sh", options.Shell.ShellBinary)
	}

	if cfg.LLM.RequestTimeout() != 42*time.Second {
		t.Fatalf("LLM request timeout = %v, want 42s", cfg.LLM.RequestTimeout())
	}
	if cfg.Agent.MaxTurns != 7 || cfg.Agent.MaxToolCalls != 11 {
		t.Fatalf("agent limits = %+v, want 7 turns and 11 tool calls", cfg.Agent)
	}
	if cfg.Agent.ToolTimeout() != 13*time.Second {
		t.Fatalf("tool timeout = %v, want 13s", cfg.Agent.ToolTimeout())
	}
}

func TestSecurityEnvOverrides(t *testing.T) {
	viper.Reset()
	defer viper.Reset()

	t.Setenv("IRIS_SECURITY_MODE", "read-only")
	t.Setenv("IRIS_SECURITY_SHELL_MODE", "deny")

	viper.SetEnvPrefix("IRIS")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	viper.SetDefault("security.mode", "workspace-write")
	viper.SetDefault("security.shell.mode", "approval")

	if err := Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	cfg := GetConfig()
	if cfg.Security.Mode != "read-only" {
		t.Fatalf("mode = %q, want env override read-only", cfg.Security.Mode)
	}
	if cfg.Security.Shell.Mode != "deny" {
		t.Fatalf("shell mode = %q, want env override deny", cfg.Security.Shell.Mode)
	}
}

func TestBuildPolicyRejectsAllowModeWithoutExternalSandbox(t *testing.T) {
	cfg := Config{
		Security: SecuritySettings{
			Shell: ShellSettings{Mode: "allow"},
		},
	}

	_, err := cfg.BuildPolicy(t.TempDir())
	if !errors.Is(err, policy.ErrInvalidPolicy) {
		t.Fatalf("BuildPolicy() error = %v, want ErrInvalidPolicy", err)
	}
}
