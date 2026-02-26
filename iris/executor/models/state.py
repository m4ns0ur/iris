from typing import Any

from pydantic import BaseModel

from iris.planner.proposal import Proposal


class ExecutionState(BaseModel):
    step_count: int = 0
    user_intent: str
    shared_context: Proposal | None = None
    state_summary: list[str] = []
    last_tool_result: Any | None = None
