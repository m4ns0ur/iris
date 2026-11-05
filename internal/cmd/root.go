package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/m4ns0ur/iris/internal/agent"
	"github.com/m4ns0ur/iris/internal/config"
	"github.com/m4ns0ur/iris/internal/llm"
	"github.com/m4ns0ur/iris/internal/logger"
	"github.com/m4ns0ur/iris/internal/tui"
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

	securityPolicy, err := config.GetConfig().BuildPolicy(workdir)
	if err != nil {
		Logger.Error("invalid security policy", "error", err)
		fmt.Fprintf(os.Stderr, "Iris cannot start: %v\n", err)
		os.Exit(1)
	}

	for _, warning := range securityPolicy.Warnings() {
		Logger.Warn("security warning", "warning", warning)
		fmt.Fprintf(os.Stderr, "iris: warning: %s\n", warning)
	}

	cfg := config.GetConfig()

	client := llm.NewClient()
	irisAgent, err := agent.NewAgent(client, securityPolicy, agent.Options{
		MaxTurns:     cfg.Agent.MaxTurns,
		MaxToolCalls: cfg.Agent.MaxToolCalls,
		ToolTimeout:  cfg.Agent.ToolTimeout(),
	})
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

	if err := tui.Run(ctx, irisAgent, securityPolicy); err != nil {
		Logger.Error("TUI failed to start", "error", err)
		// Exit uses os.Exit, which skips deferred cleanup.
		if closeErr := irisAgent.Close(); closeErr != nil {
			Logger.Warn("failed to clean up agent resources", "error", closeErr)
		}
		Exit()
	}
}

func Exit() {
	fmt.Fprintf(os.Stderr, "Iris cannot start.\nPlease check the logs for more details.\n")
	os.Exit(1)
}
