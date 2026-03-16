import json
from pathlib import Path

from jinja2 import Environment, FileSystemLoader, StrictUndefined
from pydantic import TypeAdapter

from iris.agent.models.agent_step import AgentResponse, FinalAnswer, ToolAction
from iris.agent.models.tool_state import ToolState
from iris.agent.tool_call import ToolCaller
from iris.llm.client import LLMClient
from iris.todo_maker.models.todo_critique import TodoCritique
from iris.todo_maker.models.todo_maker import TodoList
from iris.utils.json_utils import extract_json
from iris.utils.logger import logger


class TodoMaker:
    def __init__(self, workspace: Path, intent: str, critique: bool = False):
        self.workspace = workspace
        self.intent = intent
        self.critique = critique
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
        self.todo_maker_template = self.env.get_template("todo_maker.md")
        self.todo_critique_template = self.env.get_template("todo_critique.md")

    def create(self, intent: str) -> TodoList:
        state = ToolState(task=self.intent)

        while state.step_count < self.max_steps:
            state.step_count += 1

            response = self._reason(state)

            if isinstance(response, FinalAnswer):
                logger.debug(f"Step: {state.step_count}/{self.max_steps}")
                todo_list = TodoList.model_validate(response.result)
                if self.critique:
                    todo_list = self._critique(todo_list)
                return todo_list

            if isinstance(response, ToolAction):
                self.tool_caller.call(response.call, state, response)
                continue

        return TodoList(todos=[])

    def _reason(self, state: ToolState) -> FinalAnswer | ToolAction:
        last_tool_result_str = (
            json.dumps(state.last_tool_result.model_dump(), indent=2)
            if state.last_tool_result
            else ""
        )

        system_prompt = self.todo_maker_template.render(
            last_tools=state.last_tools,
            last_tool_result=last_tool_result_str,
        )
        user_prompt = f"{self.intent}"

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

        adapter: TypeAdapter[AgentResponse] = TypeAdapter(AgentResponse)
        return adapter.validate_python(data)

    def _critique(self, todo_list: TodoList) -> TodoList:
        system_prompt = self.todo_critique_template.render()
        user_prompt = f"{json.dumps(todo_list.model_dump(), indent=2)}"

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
        todo_critique = TodoCritique.model_validate(data)

        if todo_critique.valid:
            return todo_list

        if todo_critique.fix:
            return todo_critique.fix

        return TodoList(todos=[])
