import json

from jinja2 import Environment, FileSystemLoader, StrictUndefined
from pydantic import TypeAdapter

from iris.executor.models.agent_step import AgentResponse, FinalAnswer, ToolAction
from iris.executor.models.state import ExecutionState
from iris.executor.models.tool_call import ToolResult
from iris.llm.client import LLMClient
from iris.planner.models.proposal import Proposal
from iris.tools.registry import ToolRegistry
from iris.tools.shell import ALLOWED_COMMANDS
from iris.utils.json_utils import extract_json
from iris.utils.logger import logger

MAX_TOOL_OUTPUT = 2000


class Executor:
    def __init__(
        self,
        llm: LLMClient,
        tools: ToolRegistry,
    ):
        self.llm = llm
        self.tools = tools
        self.max_steps = 50
        self.env = Environment(
            loader=FileSystemLoader("iris/prompts"),
            undefined=StrictUndefined,  # fail if variable missing
            trim_blocks=True,
            lstrip_blocks=True,
            keep_trailing_newline=True,
        )

    def run(self, intent: str, shared_context: Proposal | None = None) -> str:
        state = ExecutionState(user_intent=intent, shared_context=shared_context)

        while state.step_count < self.max_steps:
            state.step_count += 1

            response = self._reason(state)

            if isinstance(response, FinalAnswer):
                if self._goal_satisfied(response):
                    return response.message
                else:
                    state.state_summary.append(
                        "Final answer rejected: goal not satisfied."
                    )
                    continue

            if isinstance(response, ToolAction):
                result = self.tools.dispatch(response.call)

                logger.debug(
                    f"Tool executed: {response.call.tool} with result: {result}"
                )

                # Truncate noisy outputs
                if result.stdout and len(result.stdout) > MAX_TOOL_OUTPUT:
                    result.stdout = result.stdout[:MAX_TOOL_OUTPUT] + "\n...[truncated]"

                # Update semantic state instead of raw logs
                self._update_state_summary(state, response, result)

                state.last_tool_result = result

                continue

        return "Execution stopped: max steps reached."

    def _reason(self, state: ExecutionState) -> FinalAnswer | ToolAction:
        template = self.env.get_template("executor.md")

        state_summary_str = (
            "\n".join(state.state_summary) if state.state_summary else "None"
        )

        last_tool_result_str = (
            json.dumps(state.last_tool_result.model_dump(), indent=2)
            if state.last_tool_result
            else "None"
        )

        prompt = template.render(
            goal_summary=state.shared_context.goal_summary
            if state.shared_context
            else "None",
            intent=state.user_intent,
            state_summary=state_summary_str,
            last_tool_result=last_tool_result_str,
        )

        raw_output = self.llm.generate(prompt=prompt)
        data = extract_json(raw_output)

        # Auto-repair raw shell commands
        if isinstance(data, dict) and data.get("type") == "tool":
            call = data.get("call", {})
            tool_name = call.get("tool")

            if tool_name in ALLOWED_COMMANDS:
                data["call"]["tool"] = "shell.run"
                data["call"]["arguments"] = {"command": [tool_name]}

        adapter: TypeAdapter[AgentResponse] = TypeAdapter(AgentResponse)
        return adapter.validate_python(data)

    def _update_state_summary(
        self, state: ExecutionState, action: ToolAction, result: ToolResult
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
            elif tool_name == "shell.run":
                state.state_summary.append("Shell command executed successfully.")
        else:
            state.state_summary.append(
                f"{tool_name} failed: {result.stderr or 'Unknown error'}"
            )

    def _goal_satisfied(self, response: FinalAnswer) -> bool:
        msg = response.message.lower()
        return not any(x in msg for x in ["should", "need to", "would need"])
