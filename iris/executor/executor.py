import json
from pathlib import Path

from jinja2 import Environment, FileSystemLoader, StrictUndefined
from pydantic import TypeAdapter

from iris.agent.models.agent_step import AgentResponse, FinalAnswer, ToolAction
from iris.agent.models.tool_state import ToolState
from iris.agent.tool_call import ToolCaller
from iris.llm.client import LLMClient
from iris.utils.json_utils import extract_json


class Executor:
    def __init__(
        self,
        workspace: Path,
        intent: str,
    ):
        self.workspace = workspace
        self.intent = intent
        self.max_steps = 50
        self.llm = LLMClient()
        self.tool_caller = ToolCaller(workspace=workspace)
        self.env = Environment(
            loader=FileSystemLoader("iris/prompts"),
            undefined=StrictUndefined,
            trim_blocks=True,
            lstrip_blocks=True,
            keep_trailing_newline=True,
        )
        self.template = self.env.get_template("executor.md")

    def run(self, task: str) -> str:
        state = ToolState(task=task)

        while state.step_count < self.max_steps:
            state.step_count += 1

            response = self._reason(state)

            if isinstance(response, FinalAnswer):
                return str(response.result)

            if isinstance(response, ToolAction):
                self.tool_caller.call(response.call, state, response)
                continue

        return "Execution stopped: max steps reached."

    def _reason(self, state: ToolState) -> FinalAnswer | ToolAction:
        state_summary_str = (
            "\n".join(state.state_summary) if state.state_summary else ""
        )

        last_tool_result_str = (
            json.dumps(state.last_tool_result.model_dump(), indent=2)
            if state.last_tool_result
            else ""
        )

        system_prompt = self.template.render(
            state_summary=state_summary_str,
            last_tool_result=last_tool_result_str,
        )

        user_prompt = f"{self.intent}\nCurrent task: {state.task}"
        raw_output = self.llm.generate(
            system_prompt=system_prompt,
            user_prompt=user_prompt,
            temperature=0.25,
            top_p=0.95,
            top_k=40,
            min_p=0.05,
            presence_penalty=0.0,
            repetition_penalty=1.1,
        )
        data = extract_json(raw_output)

        adapter: TypeAdapter[AgentResponse] = TypeAdapter(AgentResponse)
        return adapter.validate_python(data)
