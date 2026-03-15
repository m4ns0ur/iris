from typing import Annotated, Any, Literal

from pydantic import BaseModel, Field

from iris.agent.models.tool_call import ToolCall


class FinalAnswer(BaseModel):
    type: Literal["final"]
    result: Any


class ToolAction(BaseModel):
    type: Literal["tool"]
    call: ToolCall


AgentResponse = Annotated[
    FinalAnswer | ToolAction,
    Field(discriminator="type"),
]
