import subprocess

from iris.executor.models.tool_call import ToolCall, ToolResult
from iris.tools.base import Tool


class ShellTool(Tool):
    """
    Restricted shell execution tool.
    """

    ALLOWED_COMMANDS = {
        "ls",
        "pytest",
        "python",
        "pip",
        "git",
    }

    def execute(self, call: ToolCall) -> ToolResult:
        if call.tool != "shell.run":
            return ToolResult(
                tool=call.tool, success=False, stderr="Unsupported shell operation."
            )

        try:
            command: list[str] = call.arguments["command"]

            if not command:
                raise ValueError("Empty command.")

            if command[0] not in self.ALLOWED_COMMANDS:
                raise PermissionError(f"Command '{command[0]}' not allowed.")

            result = subprocess.run(
                command,
                capture_output=True,
                text=True,
                timeout=20,
            )

            return ToolResult(
                tool=call.tool,
                success=result.returncode == 0,
                stdout=result.stdout,
                stderr=result.stderr,
                metadata={"returncode": result.returncode},
            )

        except Exception as e:
            return ToolResult(
                tool=call.tool,
                success=False,
                stderr=str(e),
            )
