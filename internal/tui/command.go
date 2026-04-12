package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/m4ns0ur/iris/internal/agent"
)

// runAgenticLoopCommand starts the agent's agentic loop and streams responses
func runAgenticLoopCommand(agent *agent.Agent, input string, ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
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
