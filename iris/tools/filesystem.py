from pathlib import Path

from iris.executor.models.tool_call import ToolCall, ToolResult
from iris.tools.base import Tool


class FileSystemTool(Tool):
    """
    Sandboxed file system tool.
    """

    def __init__(self, root: str):
        self.root = Path(root).resolve()

    def _resolve(self, relative_path: str) -> Path:
        path = (self.root / relative_path).resolve()

        # Prevent path traversal
        if not str(path).startswith(str(self.root)):
            raise PermissionError("Path escapes sandbox root.")

        return path

    def execute(self, call: ToolCall) -> ToolResult:
        try:
            if call.tool == "filesystem.read":
                return self._read(call)

            if call.tool == "filesystem.write":
                return self._write(call)

            if call.tool == "filesystem.list":
                return self._list(call)

            if call.tool == "filesystem.exists":
                return self._exists(call)

            if call.tool == "filesystem.mkdir":
                return self._mkdir(call)

            return ToolResult(
                tool=call.tool,
                success=False,
                stderr="Unsupported filesystem operation.",
            )

        except Exception as e:
            return ToolResult(tool=call.tool, success=False, stderr=str(e))

    def _read(self, call: ToolCall) -> ToolResult:
        path = self._resolve(call.arguments["path"])
        content = path.read_text()
        return ToolResult(tool=call.tool, success=True, stdout=content)

    def _write(self, call: ToolCall) -> ToolResult:
        path = self._resolve(call.arguments["path"])
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(call.arguments["content"])
        return ToolResult(tool=call.tool, success=True)

    def _list(self, call: ToolCall) -> ToolResult:
        path = self._resolve(call.arguments.get("path", "."))
        items = [p.name for p in path.iterdir()]
        return ToolResult(tool=call.tool, success=True, metadata={"items": items})

    def _exists(self, call: ToolCall) -> ToolResult:
        path = self._resolve(call.arguments["path"])
        return ToolResult(
            tool=call.tool, success=True, metadata={"exists": path.exists()}
        )

    def _mkdir(self, call: ToolCall) -> ToolResult:
        path = self._resolve(call.arguments["path"])
        path.mkdir(parents=True, exist_ok=True)
        return ToolResult(tool=call.tool, success=True)
