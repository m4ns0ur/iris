package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/m4ns0ur/iris/internal/agent"
)

// runAgenticLoopCommand starts the agent's agentic loop and streams responses
func runAgenticLoopCommand(agent *agent.Agent, input string, ch chan tea.Msg) tea.Cmd {
	return runAgenticLoopCommandWithRunner(agent.Run, input, ch)
}

// runFreshAgenticLoopCommand starts a fresh agentic loop (resets state + system prompt) and streams responses
func runFreshAgenticLoopCommand(agent *agent.Agent, input string, ch chan tea.Msg) tea.Cmd {
	return runAgenticLoopCommandWithRunner(agent.RunFresh, input, ch)
}

func runAgenticLoopCommandWithRunner(runner func(input string, onChunk func(chunkType string, content string), onError func(error)) error, input string, ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		err := runner(input, func(chunkType string, content string) {
			ch <- chunkMsg{chunkType: chunkType, content: content}
		}, func(err error) {
			ch <- errMsg{error: err}
		})
		if err != nil {
			ch <- fatalErrMsg{error: err}
			return nil
		}

		// Send completion message
		ch <- doneMsg{}
		return nil
	}
}
