import subprocess

from iris.executor.models.tool_call import ToolCall, ToolResult
from iris.tools.base import Tool
from iris.utils import logger


class ShellTool(Tool):
    """
    Restricted shell execution tool.
    """

    ALLOWED_COMMANDS = {
        "ls",
        "echo",
        "pwd",
        "cat",
        "du",
        "df",
        "head",
        "tail",
        "find",
        "ps",
        "grep",
        "git",
        "python3",
        "python",
        "uv",
        "mkdir",
    }

    def __init__(self, workdir: str):
        self.workdir = workdir

    def execute(self, call: ToolCall) -> ToolResult:
        if call.tool != "shell.run":
            return ToolResult(
                tool=call.tool, success=False, stderr="Unsupported shell operation."
            )

        if not self.workdir.exists():
            raise ValueError("Workspace does not exist")

        assert self.workdir.is_dir()

        try:
            command: list[str] = call.arguments["command"]

            if not command:
                raise ValueError("Empty command.")

            if command[0] not in self.ALLOWED_COMMANDS:
                raise PermissionError(f"Command '{command[0]}' not allowed.")

            logger.debug(
                f"Running shell command in {self.workdir}: {' '.join(command)}"
            )

            result = subprocess.run(
                command,
                cwd=self.workdir,
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
