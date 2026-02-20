from pathlib import Path

from iris.executor.models.agent_step import AgentResponse, FinalAnswer, ToolAction
from iris.executor.state import ExecutionState
from iris.llm.client import LLMClient
from iris.tools.registry import ToolRegistry
from iris.utils.json_utils import extract_json


class Executor:
    def __init__(
        self,
        llm: LLMClient,
        tools: ToolRegistry,
        max_steps: int = 10,
    ):
        self.llm = llm
        self.tools = tools
        self.max_steps = max_steps

    def run(self, goal: str) -> str:
        state = ExecutionState(user_goal=goal)

        while state.step_count < self.max_steps:
            state.step_count += 1

            response = self._reason(state)

            if isinstance(response, FinalAnswer):
                return response.message

            if isinstance(response, ToolAction):
                result = self.tools.dispatch(response.call)
                state.tool_results.append(result)
                state.history.append(
                    f"Tool: {response.call.tool}, Success: {result.success}"
                )
                continue

        return "Execution stopped: max steps reached."

    def _reason(self, state: ExecutionState) -> AgentResponse:
        """
        Calls LLM and parses structured AgentResponse.
        """

        prompt_template = Path("iris/executor/prompts/executor.md").read_text()
        prompt = (
            prompt_template.replace("__GOAL__", state.user_goal)
            .replace("__HISTORY__", state.history)
            .replace("__TOOL_RESULTS__", [r.model_dump() for r in state.tool_results])
        )

        raw_output = self.llm.generate(prompt=prompt, temperature=0.2)

        data = extract_json(raw_output)
        return AgentResponse.model_validate(data)
