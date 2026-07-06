package cmd

import (
	"fmt"
	"os"
	"path/filepath"

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
		fmt.Fprintln(os.Stderr, "cannot determine the home dir:", err)
		os.Exit(1)
	}

	configDir := filepath.Join(home, IrisHome)
	viper.AddConfigPath(configDir)
	viper.SetConfigName("settings")
	viper.SetConfigType("toml")

	viper.SetEnvPrefix("IRIS")
	viper.AutomaticEnv()

	viper.SetDefault("base_url", "http://127.0.0.1:8080/v1")
	viper.SetDefault("max_retries", 3)

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			if err := os.MkdirAll(configDir, 0o755); err != nil {
				fmt.Fprintln(os.Stderr, "error creating config dir:", err)
				os.Exit(1)
			}

			if err := viper.SafeWriteConfigAs(filepath.Join(configDir, "settings.toml")); err != nil {
				if _, ok := err.(viper.ConfigFileAlreadyExistsError); !ok {
					fmt.Fprintln(os.Stderr, "error writing default config:", err)
					os.Exit(1)
				}
			}

			fmt.Fprintln(os.Stderr, "created default config")

			if err := viper.ReadInConfig(); err != nil {
				fmt.Fprintln(os.Stderr, "error reading new config:", err)
				os.Exit(1)
			}
		} else {
			fmt.Fprintln(os.Stderr, "error reading config:", err)
			os.Exit(1)
		}
	}
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		Logger.Error("Failed to execute the root command", "error", err)
		os.Exit(1)
	}
}

func startTui(cmd *cobra.Command, args []string) {
	config.Load()

	Logger.Info("Starting TUI")
	logger.SetDebug(debug)

	if workdir != "" {
		if _, err := os.Stat(workdir); os.IsNotExist(err) {
			Logger.Error("Specified working directory does not exist", "workdir", workdir)
			os.Exit(1)
		}
	} else {
		var err error
		workdir, err = os.Getwd()
		if err != nil {
			Logger.Error("Failed to get current working directory", "error", err)
			os.Exit(1)
		}
	}

	client := llm.NewClient()
	agent := agent.NewAgent(client, workdir)

	if err := tui.Run(agent); err != nil {
		Logger.Error("TUI failed to start", "error", err)
		os.Exit(1)
	}
}
