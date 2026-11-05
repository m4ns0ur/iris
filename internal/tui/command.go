package tui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"
	"github.com/m4ns0ur/iris/internal/agent"
)

// runAgenticLoopCommand starts the agent's agentic loop and streams responses.
func runAgenticLoopCommand(agent *agent.Agent, input string, ch chan tea.Msg, done <-chan struct{}) tea.Cmd {
	return runAgenticLoopCommandWithRunner(agent.RunContext, input, ch, done)
}

// runFreshAgenticLoopCommand starts a fresh agentic loop (resets state + system
// prompt) and streams responses.
func runFreshAgenticLoopCommand(agent *agent.Agent, input string, ch chan tea.Msg, done <-chan struct{}) tea.Cmd {
	return runAgenticLoopCommandWithRunner(agent.RunFreshContext, input, ch, done)
}

type agentRunner func(
	ctx context.Context,
	input string,
	onChunk func(chunkType string, content string),
	onError func(error),
) error

func runAgenticLoopCommandWithRunner(
	run agentRunner,
	input string,
	ch chan tea.Msg,
	done <-chan struct{},
) tea.Cmd {
	return func() tea.Msg {
		// Never block forever on an event that the UI can no longer consume.
		send := func(msg tea.Msg) bool {
			select {
			case ch <- msg:
				return true
			case <-done:
				return false
			}
		}

		runCtx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// If the UI shuts down, cancel the run directly rather than relying on
		// Agent.Close to do it later.
		go func() {
			select {
			case <-done:
				cancel()
			case <-runCtx.Done():
			}
		}()

		err := run(
			runCtx,
			input,
			func(chunkType string, content string) {
				send(chunkMsg{chunkType: chunkType, content: content})
			},
			func(err error) {
				send(errMsg{error: err})
			},
		)

		if err != nil {
			if errors.Is(err, context.Canceled) {
				send(canceledMsg{})
				return nil
			}

			send(fatalErrMsg{error: err})
			return nil
		}

		send(doneMsg{})
		return nil
	}
}
