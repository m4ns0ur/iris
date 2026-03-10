from typing import Any, Literal

from pydantic import BaseModel, Field


class ToolCall(BaseModel):
    tool: Literal[
        "filesystem.find",
        "filesystem.grep",
        "filesystem.stat",
        "filesystem.read_lines",
        "filesystem.read_chunk",
        "filesystem.preview_patch",
        "filesystem.patch",
        "filesystem.write",
        "code.summary",
        "shell.run",
    ]
    arguments: dict[str, Any] = Field(default_factory=dict)


class ToolResult(BaseModel):
    tool: str
    success: bool
    stdout: str | None = None
    stderr: str | None = None
    metadata: dict[str, Any] = Field(default_factory=dict)
