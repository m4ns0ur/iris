from pydantic import BaseModel

from iris.executor.models.tool_call import ToolResult


class ExecutionState(BaseModel):
    user_goal: str
    history: list[str] = []
    tool_results: list[ToolResult] = []
    step_count: int = 0
