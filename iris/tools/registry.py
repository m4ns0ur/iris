from iris.executor.models.tool_call import ToolCall, ToolResult
from iris.tools.base import Tool


class ToolRegistry:
    """
    Central tool dispatcher.
    """

    def __init__(self) -> None:
        self._tools: dict[str, Tool] = {}

    def register(self, prefix: str, tool: Tool) -> None:
        self._tools[prefix] = tool

    def dispatch(self, call: ToolCall) -> ToolResult:
        prefix = call.tool.split(".")[0]

        tool = self._tools.get(prefix)
        if not tool:
            return ToolResult(
                tool=call.tool,
                success=False,
                stderr=f"No tool registered for prefix '{prefix}'.",
            )

        return tool.execute(call)
