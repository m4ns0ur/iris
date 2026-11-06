package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/m4ns0ur/iris/agent"
)

// runAgentCommand starts one agent run. Progress and completion are delivered
// through the agent's typed event stream; no callbacks cross the package
// boundary.
func runAgentCommand(instance agent.Agent, input string, done <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		if instance == nil {
			return nil
		}

		runCtx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go func() {
			select {
			case <-done:
				cancel()
			case <-runCtx.Done():
			}
		}()

		_ = instance.Run(runCtx, input)
		return nil
	}
}
