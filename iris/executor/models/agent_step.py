from typing import Literal, Union

from pydantic import BaseModel

from iris.executor.models.tool_call import ToolCall


class FinalAnswer(BaseModel):
    type: Literal["final"]
    message: str


class ToolAction(BaseModel):
    type: Literal["tool"]
    call: ToolCall


AgentResponse = Union[FinalAnswer, ToolAction]
