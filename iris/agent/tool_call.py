import json
from pathlib import Path

from jinja2 import Environment, FileSystemLoader, StrictUndefined

from iris.agent.models.agent_step import ToolAction
from iris.agent.models.tool_call import MAX_TOOL_OUTPUT, ToolCall, ToolResult
from iris.agent.models.tool_state import ToolState
from iris.llm.client import LLMClient
from iris.tools.bash import BashTool
from iris.tools.code import CodeTool
from iris.tools.filesystem import FileSystemTool
from iris.tools.registry import ToolRegistry
from iris.utils.logger import logger


class ToolCaller:
    def __init__(self, workspace: Path) -> None:
        self.llm = LLMClient()
        self.max_steps = 50
        self.env = Environment(
            loader=FileSystemLoader("iris/prompts"),
            undefined=StrictUndefined,
            trim_blocks=True,
            lstrip_blocks=True,
            keep_trailing_newline=True,
        )

        tool_registry: ToolRegistry = ToolRegistry()

        tool_registry.register("filesystem", FileSystemTool(root=str(workspace)))
        tool_registry.register("code", CodeTool(root=str(workspace)))
        tool_registry.register("bash", BashTool(workdir=str(workspace)))

        self.tools = tool_registry

    def call(self, call: ToolCall, state: ToolState, action: ToolAction) -> None:
        result = self.tools.dispatch(call)

        logger.debug(f"Tool executed: {call.tool} with result: {result}")

        # Truncate noisy outputs
        if result.stdout and len(result.stdout) > MAX_TOOL_OUTPUT:
            result.stdout = (
                "Output was too long. Change tool usage to get more specific output."
            )

        # Update semantic state instead of raw logs
        self._update_state_summary(state, action, result)

        state.last_tool_result = result

    def _update_state_summary(
        self, state: ToolState, action: ToolAction, result: ToolResult
    ) -> None:
        tool_name = action.call.tool

        if result.success:
            if tool_name == "filesystem.find":
                if result.stdout:
                    state.state_summary.append(
                        f"Found {len(result.stdout)} files matching criteria."
                    )
            elif tool_name == "filesystem.grep":
                if result.stdout:
                    state.state_summary.append(
                        f"Found {len(result.stdout)} lines matching criteria."
                    )
            elif tool_name == "filesystem.stat":
                if result.stdout:
                    stat_info = json.loads(result.stdout)
                    state.state_summary.append(
                        f"File {action.call.arguments['path']} exists with size {stat_info.get('size', 'unknown')} bytes."
                    )
            elif tool_name == "filesystem.read_lines":
                if result.stdout:
                    state.state_summary.append(
                        f"Read lines {action.call.arguments.get('start_line', 1)} to {action.call.arguments.get('start_line', 1) + len(result.stdout)} from {action.call.arguments['path']}."
                    )
            elif tool_name == "filesystem.read_chunk":
                state.state_summary.append(
                    f"File {action.call.arguments['path']} inspected."
                )
            elif tool_name == "filesystem.preview_patch":
                if result.stdout:
                    state.state_summary.append(
                        f"Previewed patch for {action.call.arguments['path']} with {len(result.stdout.splitlines())} lines of diff."
                    )
            elif tool_name == "filesystem.patch":
                if result.stdout:
                    state.state_summary.append(
                        f"Applied patch to {action.call.arguments['path']} with {len(result.stdout.splitlines())} lines of diff."
                    )
            elif tool_name == "filesystem.write":
                state.state_summary.append(
                    f"File {action.call.arguments['path']} written."
                )
            elif tool_name == "code.summary":
                state.state_summary.append("Code summary generated.")
            elif tool_name == "bash.run":
                state.state_summary.append("Bash command executed successfully.")
        else:
            state.state_summary.append(
                f"{tool_name} failed: {result.stderr or 'Unknown error'}"
            )
