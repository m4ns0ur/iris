package prompt

import (
	"runtime"
	"time"

	"github.com/m4ns0ur/iris/internal/tool"
)

func BuildSystemPrompt(toolContext *tool.ToolContext) string {
	toolPrompt := toolContext.BuildPrompt()

	security := ""
	if toolContext.Policy != nil {
		sandboxNote := ""
		if !toolContext.Policy.ExternalSandbox() {
			sandboxNote = "- Shell commands are not OS-sandboxed; do not assume a permitted command is confined to the workspace.\n"
		}

		security = `
Security posture:
- ` + toolContext.Policy.Summary() + `
- File tools reject paths outside the allowed workspace, including symlink escapes.
- Shell commands may be denied, require user approval, or be terminated by a timeout.
` + sandboxNote + `- Never attempt to bypass a denial (for example with symlinks, subshells or nested interpreters). Explain the limitation and ask the user instead.
`
	}

	return `You are Iris, an autonomous AI agent, expert in coding and software control. Your goal is to help the user by analyzing the requests, breaking them down into smaller steps if necessary, and executing tasks using the available tools.

Tools:
` + toolPrompt + `
` + security + `
Guidelines:
- Be concise and efficient in your actions and responses. Focus on getting things done.
- Use tools to accomplish tasks, do not just describe what you would do.
- Use read, write and edit tools for file operations. Use shell tool for the rest of file operations.
- Use one edit tool call with multiple edits instead of multiple edit calls.
- Keep edits as small as possible.
- Use write tool to create a file or complete rewrite.

OS: ` + GetSimpleOS() + `
Date: ` + time.Now().Local().Format("2006-01-02") + `
Working Directory: ` + toolContext.Workdir + `
`
}

func GetSimpleOS() string {
	switch runtime.GOOS {
	case "darwin":
		return "mac"
	default:
		return runtime.GOOS
	}
}
