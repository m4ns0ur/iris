import json
from pathlib import Path

from jinja2 import Environment, FileSystemLoader, StrictUndefined

from iris.agent.models.agent_step import ToolAction
from iris.agent.models.tool_state import ToolState
from iris.agent.tool_call import ToolCaller
from iris.llm.client import LLMClient
from iris.todo_maker.models.todo_maker import TodoList
from iris.utils.json_utils import extract_json


class TodoMaker:
    def __init__(self, workspace: Path, intent: str):
        self.workspace = workspace
        self.intent = intent
        self.max_steps = 10
        self.llm = LLMClient()
        self.tool_caller = ToolCaller(workspace=workspace)
        self.env = Environment(
            loader=FileSystemLoader("iris/prompts"),
            undefined=StrictUndefined,
            trim_blocks=True,
            lstrip_blocks=True,
            keep_trailing_newline=True,
        )

    def create(self, intent: str) -> TodoList:
        state = ToolState(task=self.intent)
        state_summary_str = (
            "\n".join(state.state_summary) if state.state_summary else ""
        )

        last_tool_result_str = (
            json.dumps(state.last_tool_result.model_dump(), indent=2)
            if state.last_tool_result
            else ""
        )

        template = self.env.get_template("todo_maker.md")
        system_prompt = template.render(
            state_summary=state_summary_str,
            last_tool_result=last_tool_result_str,
        )
        user_prompt = f"{intent}"

        while state.step_count < self.max_steps:
            state.step_count += 1

            raw_output = self.llm.generate(
                system_prompt=system_prompt,
                user_prompt=user_prompt,
                temperature=0.1,
                top_p=0.9,
                top_k=40,
                min_p=0.05,
                presence_penalty=0.0,
                repetition_penalty=1.05,
            )

            data = extract_json(raw_output)
            response = TodoList.model_validate(data)

            if isinstance(response, ToolAction):
                self.tool_caller.call(response.call, state, response)
                continue

            return response

        todo_list = TodoList(todos=[])
        return todo_list
