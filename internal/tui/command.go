package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/m4ns0ur/iris/internal/agent"
)

// streamCommand handles streaming LLM responses for simple queries
func streamCommand(agent *agent.Agent, input string, ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		stream, errCh := agent.Stream(input)

		go func() {
			for chunk := range stream {
				ch <- chunkMsg(chunk)
			}

			if err := <-errCh; err != nil {
				ch <- errMsg(err)
				return
			}

			ch <- doneMsg{}
		}()

		return nil
	}
}

// runAgenticLoopCommand starts the agent's agentic loop and streams responses
func runAgenticLoopCommand(agent *agent.Agent, input string, ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		// Add user message to history
		agent.AppendUserMessage(input)

		err := agent.Run(input, func(chunk string) {
			ch <- chunkMsg(chunk)
		}, func(err error) {
			ch <- errMsg(err)
		})
		if err != nil {
			ch <- errMsg(err)
			return nil
		}

		// Send completion message
		ch <- doneMsg{}
		return nil
	}
}
