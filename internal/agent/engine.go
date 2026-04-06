package agent

import (
	"github.com/m4ns0ur/iris/internal/llm"
	"github.com/m4ns0ur/iris/internal/logger"
	"github.com/m4ns0ur/iris/internal/prompt"
)

type Agent struct {
	client      *llm.LLMClient
	state       string
	step        int
	goals       []string
	currentTask int
	history     []Message
	maxSteps    int
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Plan struct {
	Goals []string `json:"goals"`
}

type Adjustment struct {
	Type        string `json:"type"`
	TaskIndex   int    `json:"task_index,omitempty"`
	TaskIndices []int  `json:"task_indices,omitempty"`
	Description string `json:"description"`
}

var Logger = logger.Logger

func NewAgent(client *llm.LLMClient) *Agent {
	return &Agent{
		client:      client,
		state:       "idle",
		goals:       []string{},
		currentTask: -1,
		history:     []Message{},
		maxSteps:    50, // Prevent infinite loops
	}
}

// Legacy streaming interface (kept for backward compatibility)
func (a *Agent) Stream(input string) (<-chan string, <-chan error) {
	outCh := make(chan string)
	errCh := make(chan error, 1)

	go func() {
		defer close(outCh)
		defer close(errCh)

		opts := &llm.LLMOptions{
			Temperature:       0.1,
			TopP:              0.9,
			RepetitionPenalty: 1.05,
		}

		err := a.client.GenerateStream(prompt.BuildSystemPrompt(), input, opts, func(chunk string) {
			outCh <- chunk
		})

		if err != nil {
			errCh <- err
			return
		}
	}()

	return outCh, errCh
}

func (a *Agent) Run(input string) error {
	a.reset()
	// Initialize with system prompt and user input
	a.history = append(a.history, Message{Role: "user", Content: input})
	return a.loop()
}

func (a *Agent) reset() {
	a.state = "idle"
	a.step = 0
	a.goals = []string{}
	a.currentTask = -1
	a.history = []Message{}
}

func (a *Agent) loop() error {
	for a.step < a.maxSteps {
		Logger.Debug("Agent Loop", "step", a.step, "state", a.state, "currentTask", a.currentTask)

		switch a.state {
		case "idle":
			if err := a.plan(); err != nil {
				return err
			}

		case "planning":
			if err := a.executePlan(); err != nil {
				return err
			}

		case "executing":
			if err := a.runTask(); err != nil {
				return err
			}

		case "reflecting":
			if err := a.reflect(); err != nil {
				return err
			}

		case "done":
			Logger.Info("Agent loop completed")
			return nil

		default:
			Logger.Warn("Unknown state", "state", a.state)
			a.state = "idle"
		}

		if a.shouldStop() {
			break
		}

		a.step++
	}

	return nil
}

func (a *Agent) shouldStop() bool {
	if a.state == "done" {
		return true
	}

	if a.step >= a.maxSteps {
		Logger.Warn("Max steps reached")
		return true
	}

	if a.state == "executing" && !a.isTaskComplete() {
		a.state = "reflecting"
	}

	return false
}
