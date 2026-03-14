from typing import Annotated, Literal

from pydantic import BaseModel, Field

from iris.agent.models.tool_call import ToolCall


class FinalAnswer(BaseModel):
    type: Literal["final"]
    message: str


class ToolAction(BaseModel):
    type: Literal["tool"]
    call: ToolCall


AgentResponse = Annotated[
    FinalAnswer | ToolAction,
    Field(discriminator="type"),
]
