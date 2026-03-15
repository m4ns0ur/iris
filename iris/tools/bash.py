from pathlib import Path
import subprocess

from iris.agent.models.tool_call import ToolCall, ToolResult
from iris.tools.base import Tool
from iris.utils.logger import logger

MAX_STD_OUT = 8000

BLOCKED_COMMANDS = {
    "rm",
    "shutdown",
    "reboot",
    "halt",
    "poweroff",
    "mkfs",
    "dd",
    "mount",
    "umount",
    "sudo",
    "su",
    "passwd",
    "chown",
    "chmod",
    "kill",
    "killall",
    "init",
    "curl",
    "wget",
    "nc",
    "ncat",
    "telnet",
    "scp",
    "rsync",
}

BLOCKED_FLAGS = {
    "-rf",
    "-fr",
    "--no-preserve-root",
    "-delete",
}


class BashTool(Tool):
    def __init__(self, workdir: str):
        self.workdir = workdir

    def execute(self, call: ToolCall) -> ToolResult:
        if call.tool != "bash.run":
            return ToolResult(
                tool=call.tool,
                success=False,
                stderr="Unsupported bash operation.",
            )

        self._validate_workspace_path()

        try:
            command: list[str] = call.arguments.get("command", [])

            if not command:
                raise ValueError("Empty command.")

            cmd = command[0]
            if cmd in BLOCKED_COMMANDS:
                raise PermissionError(f"Command '{cmd}' is blocked.")

            self._validate_args(command)
            self._validate_paths(command)

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
                stdout=result.stdout[:MAX_STD_OUT],
                stderr=result.stderr[:MAX_STD_OUT],
                metadata={"returncode": result.returncode},
            )

        except Exception as e:
            return ToolResult(
                tool=call.tool,
                success=False,
                stderr=str(e),
            )

    def _validate_workspace_path(self) -> None:
        wd = Path(self.workdir).resolve()

        if not wd.exists():
            raise RuntimeError("Workspace does not exist")

        if not wd.is_dir():
            raise RuntimeError("Workspace must be a directory")

        # prevent running at dangerous locations
        forbidden = [Path("/"), Path.home()]

        if wd in forbidden:
            raise RuntimeError("Workspace cannot be root or home directory")

    def _validate_args(self, command: list[str]) -> None:
        for arg in command:
            if arg in BLOCKED_FLAGS:
                raise PermissionError(f"Blocked flag: {arg}")

    def _validate_paths(self, command: list[str]) -> None:
        for arg in command[1:]:
            if arg.startswith("/"):
                raise PermissionError("Absolute paths are not allowed")
