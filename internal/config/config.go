package config

import (
	"strings"
	"time"

	"github.com/m4ns0ur/iris/internal/logger"
	"github.com/m4ns0ur/iris/internal/policy"
	"github.com/m4ns0ur/iris/provider"
	"github.com/spf13/viper"
)

// Config represents the whole settings.toml
type Config struct {
	BaseURL      string           `mapstructure:"base_url"`
	MaxRetries   int              `mapstructure:"max_retries"`
	DefaultModel string           `mapstructure:"default_model"`
	Models       []ModelSettings  `mapstructure:"models"`
	Security     SecuritySettings `mapstructure:"security"`
	LLM          LLMSettings      `mapstructure:"llm"`
	Agent        AgentSettings    `mapstructure:"agent"`
	Session      SessionSettings  `mapstructure:"session"`
	Context      ContextSettings  `mapstructure:"context"`
}

// ModelSettings represents one [models.'<name>'] block
type ModelSettings struct {
	Name               string   `mapstructure:"name"`
	CtxSize            int      `mapstructure:"context_size"`
	ToolCalling        bool     `mapstructure:"tool_calling"`
	Reasoning          bool     `mapstructure:"reasoning"`
	StructuredOutput   bool     `mapstructure:"structured_output"`
	Vision             bool     `mapstructure:"vision"`
	SamplingParameters []string `mapstructure:"sampling_parameters"`
	Temperature        float32  `mapstructure:"temperature"`
	TopP               float32  `mapstructure:"top_p"`
	TopK               int      `mapstructure:"top_k"`
	MinP               float32  `mapstructure:"min_p"`
	RepeatPenalty      float32  `mapstructure:"repeat_penalty"`
	PresencePenalty    float32  `mapstructure:"presence_penalty"`
	FrequencyPenalty   float32  `mapstructure:"frequency_penalty"`
}

// Capabilities returns descriptive model capabilities. They are metadata for
// now; provider integrations can begin honoring them as concrete needs arise.
func (m ModelSettings) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		ContextWindow:      m.CtxSize,
		ToolCalling:        m.ToolCalling,
		Reasoning:          m.Reasoning,
		StructuredOutput:   m.StructuredOutput,
		Vision:             m.Vision,
		SamplingParameters: append([]string(nil), m.SamplingParameters...),
	}
}

// LLMSettings controls LLM request lifecycle.
type LLMSettings struct {
	// RequestTimeoutSeconds bounds a complete LLM request, including retries
	// and stream reading. Zero means "use the client default".
	RequestTimeoutSeconds int `mapstructure:"request_timeout_seconds"`
}

// RequestTimeout converts the configured seconds to a duration.
func (s LLMSettings) RequestTimeout() time.Duration {
	if s.RequestTimeoutSeconds <= 0 {
		return 0
	}
	return time.Duration(s.RequestTimeoutSeconds) * time.Second
}

// AgentSettings controls per-run execution budgets.
type AgentSettings struct {
	// MaxTurns bounds the number of LLM turns in one run.
	MaxTurns int `mapstructure:"max_turns"`

	// MaxToolCalls bounds the number of tool executions in one run.
	MaxToolCalls int `mapstructure:"max_tool_calls"`

	// ToolTimeoutSeconds bounds a single tool execution.
	ToolTimeoutSeconds int `mapstructure:"tool_timeout_seconds"`
}

// ToolTimeout converts the configured seconds to a duration.
func (s AgentSettings) ToolTimeout() time.Duration {
	if s.ToolTimeoutSeconds <= 0 {
		return 0
	}
	return time.Duration(s.ToolTimeoutSeconds) * time.Second
}

// ContextSettings controls prompt construction and compaction.
type ContextSettings struct {
	// Window overrides the model context_size. Zero uses the model setting.
	Window int `mapstructure:"window"`

	// ReserveTokens is reserved for the next assistant response.
	ReserveTokens int `mapstructure:"reserve_tokens"`

	// CompactAt is the fraction of the prompt budget that triggers compaction.
	CompactAt float64 `mapstructure:"compact_at"`

	// KeepRecentTokens is kept verbatim during compaction.
	KeepRecentTokens int `mapstructure:"keep_recent_tokens"`

	// SummaryMaxTokens bounds generated summaries.
	SummaryMaxTokens int `mapstructure:"summary_max_tokens"`
}

// SessionSettings controls persistent session storage.
type SessionSettings struct {
	// Dir is the session store directory. Empty uses ~/.iris/sessions.
	Dir string `mapstructure:"dir"`
}

// SecuritySettings controls the execution boundary for file and shell tools.
type SecuritySettings struct {
	// Mode is read-only, workspace-write or danger-full-access.
	Mode string `mapstructure:"mode"`

	// ReadPaths and WritePaths grant access outside the workspace.
	ReadPaths  []string `mapstructure:"read_paths"`
	WritePaths []string `mapstructure:"write_paths"`

	// ExternalSandbox acknowledges that Iris runs inside a container or VM.
	// It is required for shell mode "allow".
	ExternalSandbox bool `mapstructure:"external_sandbox"`

	// Shell configures command execution.
	Shell ShellSettings `mapstructure:"shell"`
}

// ShellSettings is the config-file representation of policy.ShellOptions.
type ShellSettings struct {
	Mode                   string   `mapstructure:"mode"`
	Allow                  []string `mapstructure:"allow"`
	Deny                   []string `mapstructure:"deny"`
	DisableDefaultDeny     bool     `mapstructure:"disable_default_deny"`
	ApprovalHook           []string `mapstructure:"approval_hook"`
	TimeoutSeconds         int      `mapstructure:"timeout_seconds"`
	ApprovalTimeoutSeconds int      `mapstructure:"approval_timeout_seconds"`
	MaxOutputBytes         int      `mapstructure:"max_output_bytes"`
	MaxOutputLines         int      `mapstructure:"max_output_lines"`
	MaxCaptureBytes        int      `mapstructure:"max_capture_bytes"`
	TempFileTTLMinutes     int      `mapstructure:"temp_file_ttl_minutes"`
	ShellBinary            string   `mapstructure:"binary"`
}

var (
	Logger = logger.Logger
	cfg    Config
)

func Load() error {
	cfg = Config{}
	return viper.Unmarshal(&cfg)
}

func GetConfig() Config {
	return cfg
}

// PolicyOptions converts the security section into policy options. Zero
// values are intentionally passed through so that policy applies its own
// defaults.
func (c Config) PolicyOptions() policy.Options {
	shell := c.Security.Shell

	return policy.Options{
		Mode:       policy.Mode(c.Security.Mode),
		ReadPaths:  append([]string(nil), c.Security.ReadPaths...),
		WritePaths: append([]string(nil), c.Security.WritePaths...),
		Shell: policy.ShellOptions{
			Mode:               policy.ShellMode(shell.Mode),
			Allow:              append([]string(nil), shell.Allow...),
			Deny:               append([]string(nil), shell.Deny...),
			DisableDefaultDeny: shell.DisableDefaultDeny,
			ApprovalHook:       append([]string(nil), shell.ApprovalHook...),
			Timeout:            seconds(shell.TimeoutSeconds),
			ApprovalTimeout:    seconds(shell.ApprovalTimeoutSeconds),
			MaxOutputBytes:     shell.MaxOutputBytes,
			MaxOutputLines:     shell.MaxOutputLines,
			MaxCaptureBytes:    shell.MaxCaptureBytes,
			TempDirTTL:         minutes(shell.TempFileTTLMinutes),
			ShellBinary:        shell.ShellBinary,
		},
		ExternalSandbox: c.Security.ExternalSandbox,
	}
}

// BuildPolicy creates the security policy for a workspace.
func (c Config) BuildPolicy(workdir string) (*policy.Policy, error) {
	return policy.New(workdir, c.PolicyOptions())
}

func seconds(value int) time.Duration {
	if value <= 0 {
		return 0
	}
	return time.Duration(value) * time.Second
}

func minutes(value int) time.Duration {
	if value <= 0 {
		return 0
	}
	return time.Duration(value) * time.Minute
}

func DefaultModel() ModelSettings {
	for _, modelSettings := range cfg.Models {
		if strings.EqualFold(modelSettings.Name, cfg.DefaultModel) {
			return modelSettings
		}
	}
	Logger.Error("no settings found for default_model", "default_model", cfg.DefaultModel)
	return ModelSettings{}
}
