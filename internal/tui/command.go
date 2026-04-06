package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/m4ns0ur/iris/internal/agent"
)

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
