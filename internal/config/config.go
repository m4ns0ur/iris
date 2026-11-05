package config

import (
	"strings"
	"time"

	"github.com/m4ns0ur/iris/internal/logger"
	"github.com/m4ns0ur/iris/internal/policy"
	"github.com/spf13/viper"
)

// Config represents the whole settings.toml
type Config struct {
	BaseURL      string           `mapstructure:"base_url"`
	MaxRetries   int              `mapstructure:"max_retries"`
	DefaultModel string           `mapstructure:"default_model"`
	Models       []ModelSettings  `mapstructure:"models"`
	Security     SecuritySettings `mapstructure:"security"`
}

// ModelSettings represents one [models.'<name>'] block
type ModelSettings struct {
	Name             string  `mapstructure:"name"`
	CtxSize          int     `mapstructure:"context_size"`
	Temperature      float32 `mapstructure:"temperature"`
	TopP             float32 `mapstructure:"top_p"`
	TopK             int     `mapstructure:"top_k"`
	MinP             float32 `mapstructure:"min_p"`
	RepeatPenalty    float32 `mapstructure:"repeat_penalty"`
	PresencePenalty  float32 `mapstructure:"presence_penalty"`
	FrequencyPenalty float32 `mapstructure:"frequency_penalty"`
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
