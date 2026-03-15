from typing import Any

from pydantic import BaseModel


class ToolState(BaseModel):
    step_count: int = 0
    task: str
    last_tools: list[str] = []
    last_tool_result: Any | None = None
