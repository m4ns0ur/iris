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

// instructionKind selects how a message entered while a run is active is
// delivered.
type instructionKind int

const (
	instructionSteer instructionKind = iota
	instructionFollowUp
)

// instructionResultMsg reports whether a streamed instruction was accepted.
type instructionResultMsg struct {
	kind instructionKind
	text string
	err  error
}

// submitInstructionCommand queues a steering instruction or follow-up without
// blocking the TUI event loop.
func submitInstructionCommand(instance agent.Agent, kind instructionKind, text string) tea.Cmd {
	return func() tea.Msg {
		if instance == nil {
			return instructionResultMsg{kind: kind, text: text, err: agent.ErrAgentIdle}
		}

		var err error
		switch kind {
		case instructionSteer:
			err = instance.Steer(text)
		case instructionFollowUp:
			err = instance.FollowUp(text)
		default:
			err = agent.ErrAgentIdle
		}
		return instructionResultMsg{kind: kind, text: text, err: err}
	}
}

// abortResultMsg carries instructions recovered by an abort so the TUI can
// restore them to the input box.
type abortResultMsg struct {
	steering  []string
	followUps []string
}

// abortRunCommand clears the pending queue and aborts the run atomically. The
// agent stops accepting instructions before canceling, so every queued message
// is either returned to the TUI or delivered, never silently lost.
func abortRunCommand(instance agent.Agent) tea.Cmd {
	return func() tea.Msg {
		if instance == nil {
			return abortResultMsg{}
		}

		queue := instance.Abort()
		return abortResultMsg{steering: queue.Steering, followUps: queue.FollowUps}
	}
}
