package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/m4ns0ur/iris/agent"
	"github.com/m4ns0ur/iris/internal/config"
	"github.com/m4ns0ur/iris/internal/llm"
	"github.com/m4ns0ur/iris/internal/logger"
	"github.com/m4ns0ur/iris/internal/tui"
	"github.com/m4ns0ur/iris/runtime"
	"github.com/m4ns0ur/iris/session"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const IrisHome = ".iris"

var (
	Logger  = logger.Logger
	debug   bool
	workdir string
	baseURL string
	model   string
)

var rootCmd = &cobra.Command{
	Use:   "iris",
	Short: "iris is an AI agent that can perform various tasks based on user input.",
	Run:   startTui,
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().BoolVarP(&debug, "debug", "d", false, "Enable debug logging")
	rootCmd.PersistentFlags().StringVarP(&workdir, "workdir", "w", "", "Set the working directory for the agent (Defaults to current directory)")
	rootCmd.PersistentFlags().StringVarP(&baseURL, "base-url", "b", "", "Set the LLM API URL (Defaults to http://127.0.0.1:8080/v1)")
	rootCmd.PersistentFlags().StringVarP(&model, "model", "m", "", "Set the LLM model name (e.g. unsloth/Qwen3.5-9B-GGUF:UD-Q5_K_XL)")

	_ = viper.BindPFlag("base_url", rootCmd.PersistentFlags().Lookup("base-url"))
	_ = viper.BindPFlag("default_model", rootCmd.PersistentFlags().Lookup("model"))
}

func initConfig() {
	home, err := os.UserHomeDir()
	if err != nil {
		Logger.Error("cannot determine the home dir:", "error", err)
		Exit()
	}

	configDir := filepath.Join(home, IrisHome)
	viper.AddConfigPath(configDir)
	viper.SetConfigName("settings")
	viper.SetConfigType("toml")

	viper.SetEnvPrefix("IRIS")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	viper.SetDefault("base_url", "http://127.0.0.1:8080/v1")
	viper.SetDefault("max_retries", 3)

	// Security defaults are repeated here so that IRIS_SECURITY_* environment
	// variables work with AutomaticEnv (Viper only resolves known keys).
	viper.SetDefault("security.mode", "workspace-write")
	viper.SetDefault("security.external_sandbox", false)
	viper.SetDefault("security.shell.mode", "approval")
	viper.SetDefault("security.shell.timeout_seconds", 120)
	viper.SetDefault("security.shell.approval_timeout_seconds", 300)
	viper.SetDefault("security.shell.max_output_bytes", 25*1024)
	viper.SetDefault("security.shell.max_output_lines", 1000)
	viper.SetDefault("security.shell.max_capture_bytes", 10*1024*1024)
	viper.SetDefault("security.shell.temp_file_ttl_minutes", 24*60)

	viper.SetDefault("llm.request_timeout_seconds", int(llm.DefaultRequestTimeout.Seconds()))
	viper.SetDefault("agent.max_turns", agent.DefaultMaxTurns)
	viper.SetDefault("agent.max_tool_calls", agent.DefaultMaxToolCalls)
	viper.SetDefault("agent.tool_timeout_seconds", int(agent.DefaultToolTimeout.Seconds()))
	viper.SetDefault("session.dir", "")

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			if err := os.MkdirAll(configDir, 0o755); err != nil {
				Logger.Error("error creating config dir:", "error", err)
				Exit()
			}

			if err := viper.SafeWriteConfigAs(filepath.Join(configDir, "settings.toml")); err != nil {
				if _, ok := err.(viper.ConfigFileAlreadyExistsError); !ok {
					Logger.Error("error writing default config:", "error", err)
					Exit()
				}
			}

			Logger.Debug("created default config", "path", filepath.Join(configDir, "settings.toml"))

			if err := viper.ReadInConfig(); err != nil {
				Logger.Error("error reading new config:", "error", err)
				Exit()
			}
		} else {
			Logger.Error("error reading config:", "error", err)
			Exit()
		}
	}
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		Logger.Error("Failed to execute the root command", "error", err)
		Exit()
	}
}

func startTui(cmd *cobra.Command, args []string) {
	if err := config.Load(); err != nil {
		Logger.Error("error unmarshaling config", "error", err)
		Exit()
	}

	Logger.Info("Starting TUI")
	logger.SetDebug(debug)

	if workdir != "" {
		if _, err := os.Stat(workdir); os.IsNotExist(err) {
			Logger.Error("Specified working directory does not exist", "workdir", workdir)
			Exit()
		}
	} else {
		var err error
		workdir, err = os.Getwd()
		if err != nil {
			Logger.Error("Failed to get current working directory", "error", err)
			Exit()
		}
	}

	cfg := config.GetConfig()

	sessionDir := cfg.Session.Dir
	if sessionDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			Logger.Error("cannot determine home directory for sessions", "error", err)
			fmt.Fprintf(os.Stderr, "Iris cannot start: %v\n", err)
			os.Exit(1)
		}
		sessionDir = filepath.Join(home, IrisHome, "sessions")
	}

	sessions, err := session.NewJSONL(sessionDir)
	if err != nil {
		Logger.Error("cannot initialize session store", "error", err)
		fmt.Fprintf(os.Stderr, "Iris cannot start: %v\n", err)
		os.Exit(1)
	}

	sessionDefaults := session.CreateOptions{
		Model:         cfg.DefaultModel,
		Workspace:     workdir,
		ModelMetadata: modelMetadata(cfg),
	}

	irt, err := runtime.New(runtime.Options{
		Workspace:    workdir,
		Provider:     llm.NewClient(),
		Model:        cfg.DefaultModel,
		MaxTurns:     cfg.Agent.MaxTurns,
		MaxToolCalls: cfg.Agent.MaxToolCalls,
		ToolTimeout:  cfg.Agent.ToolTimeout(),
		Security:     runtimeSecurity(cfg.Security),
	})
	if err != nil {
		Logger.Error("invalid runtime configuration", "error", err)
		fmt.Fprintf(os.Stderr, "Iris cannot start: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if err := irt.Close(); err != nil {
			Logger.Warn("failed to clean up runtime resources", "error", err)
		}
	}()

	for _, warning := range irt.Warnings() {
		Logger.Warn("security warning", "warning", warning)
		fmt.Fprintf(os.Stderr, "iris: warning: %s\n", warning)
	}

	irisAgent, err := irt.NewAgent()
	if err != nil {
		Logger.Error("failed to create agent", "error", err)
		fmt.Fprintf(os.Stderr, "Iris cannot start: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if err := irisAgent.Close(); err != nil {
			Logger.Warn("failed to clean up agent resources", "error", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := tui.Run(ctx, tui.Options{
		Agent:           irisAgent,
		Sessions:        sessions,
		SessionDefaults: sessionDefaults,
	}); err != nil {
		Logger.Error("TUI failed to start", "error", err)
		// Exit uses os.Exit, which skips deferred cleanup.
		if closeErr := irisAgent.Close(); closeErr != nil {
			Logger.Warn("failed to clean up agent resources", "error", closeErr)
		}
		if closeErr := irt.Close(); closeErr != nil {
			Logger.Warn("failed to clean up runtime resources", "error", closeErr)
		}
		Exit()
	}
}

func modelMetadata(cfg config.Config) map[string]string {
	metadata := map[string]string{
		"provider": "openai-compatible",
		"base_url": cfg.BaseURL,
	}

	model := config.DefaultModel()
	if model.CtxSize > 0 {
		metadata["context_size"] = strconv.Itoa(model.CtxSize)
	}
	if model.Temperature != 0 {
		metadata["temperature"] = strconv.FormatFloat(float64(model.Temperature), 'f', -1, 32)
	}
	if model.TopP != 0 {
		metadata["top_p"] = strconv.FormatFloat(float64(model.TopP), 'f', -1, 32)
	}
	if model.TopK != 0 {
		metadata["top_k"] = strconv.Itoa(model.TopK)
	}
	if model.MinP != 0 {
		metadata["min_p"] = strconv.FormatFloat(float64(model.MinP), 'f', -1, 32)
	}
	if model.RepeatPenalty != 0 {
		metadata["repeat_penalty"] = strconv.FormatFloat(float64(model.RepeatPenalty), 'f', -1, 32)
	}
	if model.PresencePenalty != 0 {
		metadata["presence_penalty"] = strconv.FormatFloat(float64(model.PresencePenalty), 'f', -1, 32)
	}
	if model.FrequencyPenalty != 0 {
		metadata["frequency_penalty"] = strconv.FormatFloat(float64(model.FrequencyPenalty), 'f', -1, 32)
	}

	return metadata
}

func runtimeSecurity(security config.SecuritySettings) runtime.Security {
	shell := security.Shell

	return runtime.Security{
		Mode:            security.Mode,
		ExternalSandbox: security.ExternalSandbox,
		ReadPaths:       append([]string(nil), security.ReadPaths...),
		WritePaths:      append([]string(nil), security.WritePaths...),
		Shell: runtime.ShellSecurity{
			Mode:               shell.Mode,
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
			Binary:             shell.ShellBinary,
		},
	}
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

func Exit() {
	fmt.Fprintf(os.Stderr, "Iris cannot start.\nPlease check the logs for more details.\n")
	os.Exit(1)
}
