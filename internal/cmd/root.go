package cmd

import (
	"os"

	"github.com/m4ns0ur/iris/internal/agent"
	"github.com/m4ns0ur/iris/internal/llm"
	"github.com/m4ns0ur/iris/internal/logger"
	"github.com/m4ns0ur/iris/internal/tui"
	"github.com/spf13/cobra"
)

var Logger = logger.Logger

var (
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

	logger.SetDebug(debug)
}

func startTui(cmd *cobra.Command, args []string) {
	client := llm.NewClient("")
	agent := agent.NewAgent(client, workdir)

	if err := tui.Run(agent); err != nil {
		Logger.Error("Bubble Tea program failed", "error", err)
		os.Exit(1)
	}
}

//excute
// temperature=0.25,
// top_p=0.95,
// top_k=40,
// min_p=0.05,
// presence_penalty=0.0,
// repetition_penalty=1.1,

// json utils
// import json
// import re
// from typing import Any

// def extract_json(text: str) -> Any:
//     text = text.strip()

//     # fast path
//     if text.startswith("{") and text.endswith("}"):
//         return json.loads(text)

//     # remove markdown fences but keep content
//     text = re.sub(r"```(?:json)?", "", text)
//     text = text.replace("```", "")

//     start = text.find("{")
//     if start == -1:
//         raise ValueError("No JSON found")

//     decoder = json.JSONDecoder()
//     obj, _ = decoder.raw_decode(text[start:])
//     return obj
