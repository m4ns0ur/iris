from typing import Any, Literal

from pydantic import BaseModel, Field


class ToolCall(BaseModel):
    """
    Structured tool invocation request produced by Iris.
    """

    tool: Literal[
        "filesystem.read",
        "filesystem.write",
        "filesystem.list",
        "filesystem.exists",
        "filesystem.mkdir",
        "shell.run",
    ]
    arguments: dict[str, Any] = Field(default_factory=dict)


class ToolResult(BaseModel):
    """
    Deterministic tool execution result.
    """

    tool: str
    success: bool
    stdout: str | None = None
    stderr: str | None = None
    metadata: dict[str, Any] = Field(default_factory=dict)
