package main

import (
	"fmt"
	"os"

	"github.com/m4ns0ur/iris/internal/llm"
	"github.com/m4ns0ur/iris/internal/logger"
)

var Logger = logger.Logger

func main() {
	llmClient := llm.NewClient("")

	systemPrompt := `You are Iris, an autonomous agent.

You get the user input and generate a prompt for a agent to work on it.

Rules:
  - Prompt must be concise and effective`

	userPrompt := "create a snake game in js"

	err := llmClient.GenerateStream(systemPrompt, userPrompt, &llm.LLMOptions{
		Temperature:       0.1,
		TopP:              0.9,
		TopK:              40,
		MinP:              0.05,
		PresencePenalty:   0.0,
		RepetitionPenalty: 1.05,
	}, func(chunk string) {
		fmt.Print(chunk)
	})
	if err != nil {
		Logger.Error("LLM generation failed", "error", err)
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
