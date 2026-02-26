import json
from pathlib import Path

from pydantic import TypeAdapter

from iris.executor.models.agent_step import AgentResponse, FinalAnswer, ToolAction
from iris.executor.models.state import ExecutionState
from iris.llm.client import LLMClient
from iris.planner.proposal import Proposal
from iris.tools.registry import ToolRegistry
from iris.tools.shell import ALLOWED_COMMANDS
from iris.utils.json_utils import extract_json
from iris.executor.models.tool_call import ToolResult

MAX_TOOL_OUTPUT = 2000


class Executor:
    def __init__(
        self,
        llm: LLMClient,
        tools: ToolRegistry,
        task_type: str,
    ):
        self.llm = llm
        self.tools = tools
        self.task_type = task_type

        if self.task_type == "atomic":
            self.max_steps = 5
        elif self.task_type == "multi_step":
            self.max_steps = 15
        else:
            self.max_steps = 50

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

                # Truncate noisy outputs
                if result.stdout and len(result.stdout) > MAX_TOOL_OUTPUT:
                    result.stdout = result.stdout[:MAX_TOOL_OUTPUT] + "\n...[truncated]"

                # Update semantic state instead of raw logs
                self._update_state_summary(state, response, result)

                # Keep ONLY last relevant tool result if needed
                if not result.success or result.stdout:
                    state.last_tool_result = result
                else:
                    state.last_tool_result = None

                continue

        return "Execution stopped: max steps reached."

    def _reason(self, state: ExecutionState) -> AgentResponse:
        if self.task_type == "atomic":
            prompt_template = Path(
                "iris/executor/prompts/atomic_executor.md"
            ).read_text()
        else:
            prompt_template = Path("iris/executor/prompts/executor.md").read_text()

        state_summary_str = (
            "\n".join(state.state_summary) if state.state_summary else "None"
        )

        last_tool_result_str = (
            json.dumps(state.last_tool_result.model_dump(), indent=2)
            if state.last_tool_result
            else "None"
        )

        prompt = (
            prompt_template.replace("[INTENT]", state.user_intent)
            .replace("[STATE_SUMMARY]", state_summary_str)
            .replace("[LAST_TOOL_RESULT]", last_tool_result_str)
        )

        raw_output = self.llm.generate(prompt=prompt, temperature=0.2)
        data = extract_json(raw_output)

        # Auto-repair raw shell commands
        if data.get("type") == "tool":
            call = data.get("call", {})
            tool_name = call.get("tool")

            if tool_name in ALLOWED_COMMANDS:
                data["call"]["tool"] = "shell.run"
                data["call"]["arguments"] = {"command": [tool_name]}

        adapter = TypeAdapter(AgentResponse)
        return adapter.validate_python(data)

    def _update_state_summary(
        self, state: ExecutionState, action: ToolAction, result: ToolResult
    ) -> None:
        """
        Convert tool events into semantic state.
        No raw logs.
        """

        tool_name = action.call.tool

        if result.success:
            if tool_name == "filesystem.write":
                state.state_summary.append("File written successfully.")
            elif tool_name == "filesystem.mkdir":
                state.state_summary.append("Directory created.")
            elif tool_name == "filesystem.read":
                state.state_summary.append("File content inspected.")
            elif tool_name == "shell.run":
                state.state_summary.append("Shell command executed successfully.")
            else:
                state.state_summary.append(f"{tool_name} executed successfully.")
        else:
            state.state_summary.append(
                f"{tool_name} failed: {result.stderr or 'Unknown error'}"
            )

    def _goal_satisfied(self, response: FinalAnswer) -> bool:
        msg = response.message.lower()
        return not any(x in msg for x in ["should", "need to", "would need"])
