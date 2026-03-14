from abc import ABC, abstractmethod

from iris.agent.models.tool_call import ToolCall, ToolResult


class Tool(ABC):
    name: str

    @abstractmethod
    def execute(self, call: ToolCall) -> ToolResult:
        pass
