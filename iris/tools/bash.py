from pathlib import Path
import subprocess

from iris.agent.models.tool_call import ToolCall, ToolResult
from iris.tools.base import Tool
from iris.utils.logger import logger

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


class BashTool(Tool):
    def __init__(self, workdir: str):
        self.workdir = workdir

    def execute(self, call: ToolCall) -> ToolResult:
        if call.tool != "bash.run":
            return ToolResult(
                tool=call.tool, success=False, stderr="Unsupported bash operation."
            )

        assert self.workdir and Path(self.workdir).exists(), (
            "Workspace directory does not exist"
        )
        assert Path(self.workdir).is_dir(), "Workspace must be a directory"

        try:
            command: list[str] = call.arguments.get("command", [])

            if not command:
                raise ValueError("Empty command.")

            if command[0] not in ALLOWED_COMMANDS:
                raise PermissionError(f"Command '{command[0]}' not allowed.")

            logger.debug(f"Running bash command in {self.workdir}: {' '.join(command)}")

            result = subprocess.run(
                command,
                cwd=self.workdir,
                capture_output=True,
                text=True,
                timeout=20,
            )

            logger.debug(
                f"Command stdout: {result.stdout}\nCommand stderr: {result.stderr}"
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
