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
)

var rootCmd = &cobra.Command{
	Use:   "iris",
	Short: "iris is an AI agent that can perform various tasks based on user input.",
	Run:   startTui,
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&debug, "debug", "d", false, "Enable debug logging")
	rootCmd.PersistentFlags().StringVarP(&workdir, "workdir", "w", "", "Set the working directory for the agent")
}

func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		Logger.Error("Failed to execute command", "error", err)
		os.Exit(1)
	}
}

func startTui(cmd *cobra.Command, args []string) {
	logger.SetDebug(debug)

	client := llm.NewClient("")
	agent := agent.NewAgent(client, workdir)

	if err := tui.Run(agent); err != nil {
		Logger.Error("Bubble Tea program failed", "error", err)
		os.Exit(1)
	}
}
