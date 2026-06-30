package cmd

import (
	"os"

	"github.com/m4ns0ur/iris/internal/agent"
	"github.com/m4ns0ur/iris/internal/llm"
	"github.com/m4ns0ur/iris/internal/logger"
	"github.com/m4ns0ur/iris/internal/tui"
	"github.com/spf13/cobra"
)

var (
	Logger  = logger.Logger
	debug   bool
	workdir string
	model   string
)

var rootCmd = &cobra.Command{
	Use:   "iris",
	Short: "iris is an AI agent that can perform various tasks based on user input.",
	Run:   startTui,
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&debug, "debug", "d", false, "Enable debug logging")
	rootCmd.PersistentFlags().StringVarP(&workdir, "workdir", "w", "", "Set the working directory for the agent")
	rootCmd.PersistentFlags().StringVarP(&model, "model", "m", "", "Set the LLM model name (e.g. unsloth/Qwen3.5-9B-GGUF:UD-Q5_K_XL)")
}

func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		Logger.Error("Failed to execute the root command", "error", err)
		os.Exit(1)
	}
}

func startTui(cmd *cobra.Command, args []string) {
	Logger.Info("Starting TUI")
	logger.SetDebug(debug)

	if workdir != "" {
		if _, err := os.Stat(workdir); os.IsNotExist(err) {
			Logger.Error("Specified working directory does not exist", "workdir", workdir)
			os.Exit(1)
		}
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			Logger.Error("Failed to get current working directory", "error", err)
			os.Exit(1)
		}
		workdir = cwd
	}

	client := llm.NewClient(model)
	agent := agent.NewAgent(client, workdir)

	if err := tui.Run(agent); err != nil {
		Logger.Error("TUI failed to start", "error", err)
		os.Exit(1)
	}
}
