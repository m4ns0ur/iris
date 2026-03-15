import fnmatch
from pathlib import Path
import re

from iris.agent.models.tool_call import ToolCall, ToolResult
from iris.tools.base import Tool
from iris.tools.patch import apply_search_replace_patch

MAX_FIND_RESULTS = 200
MAX_GREP_RESULTS = 200
MAX_READ_LINES = 200
MAX_PATCH_LINES = 200
CHUNK_SIZE = 2000


class FileSystemTool(Tool):
    def __init__(self, root: str):
        self.root = Path(root).resolve()

    def _resolve(self, path: str) -> Path:
        candidate = (self.root / path).resolve()

        if not str(candidate).startswith(str(self.root)):
            raise ValueError("Access outside workspace is not allowed")

        return candidate

    def execute(self, call: ToolCall) -> ToolResult:
        try:
            if call.tool == "filesystem.read_chunk":
                return self._read_chunk(call)

            if call.tool == "filesystem.read_lines":
                return self._read_lines(call)

            if call.tool == "filesystem.write":
                return self._write(call)

            if call.tool == "filesystem.patch":
                return self._patch(call)

            if call.tool == "filesystem.stat":
                return self._stat(call)

            if call.tool == "filesystem.find":
                return self._find(call)

            if call.tool == "filesystem.grep":
                return self._grep(call)

            return ToolResult(
                tool=call.tool,
                success=False,
                stderr="Unsupported filesystem operation.",
            )

        except Exception as e:
            return ToolResult(tool=call.tool, success=False, stderr=str(e))

    def _read_chunk(self, call: ToolCall) -> ToolResult:
        path = self._resolve(call.arguments.get("path", ""))

        if not path.exists():
            return ToolResult(
                tool=call.tool,
                success=False,
                stderr="File does not exist.",
            )

        start = call.arguments.get("start", 0)
        file_size = path.stat().st_size

        if start >= file_size:
            return ToolResult(
                tool=call.tool,
                success=True,
                stdout="",
                metadata={
                    "path": str(path.relative_to(self.root)),
                    "start": start,
                    "bytes_read": 0,
                    "file_size": file_size,
                    "next_start": start,
                    "eof": True,
                },
            )

        with Path.open(path, "rb") as f:
            f.seek(start)
            data = f.read(CHUNK_SIZE)

        bytes_read = len(data)
        eof = (start + bytes_read) >= file_size
        next_start = start + bytes_read

        content = data.decode("utf-8", errors="replace")

        return ToolResult(
            tool=call.tool,
            success=True,
            stdout=content,
            metadata={
                "path": str(path.relative_to(self.root)),
                "start": start,
                "bytes_read": bytes_read,
                "file_size": file_size,
                "next_start": next_start,
                "eof": eof,
            },
        )

    def _read_lines(self, call: ToolCall) -> ToolResult:
        path = self._resolve(call.arguments.get("path", ""))

        if not path.exists():
            return ToolResult(
                tool=call.tool, success=False, stderr="File does not exist."
            )

        start = max(call.arguments.get("start", 1), 1)
        count = call.arguments.get("num", MAX_READ_LINES)

        with Path.open(path, encoding="utf-8", errors="replace") as f:
            lines = f.readlines()

        total_lines = len(lines)
        selected = lines[start - 1 : start - 1 + count]

        return ToolResult(
            tool=call.tool,
            success=True,
            stdout="".join(selected),
            metadata={
                "path": str(path.relative_to(self.root)),
                "start_line": start,
                "lines_returned": len(selected),
                "total_lines": total_lines,
                "eof": start - 1 + count >= total_lines,
            },
        )

    def _stat(self, call: ToolCall) -> ToolResult:
        path = self._resolve(call.arguments.get("path", ""))

        if not path.exists():
            return ToolResult(
                tool=call.tool, success=False, stderr="Path does not exist."
            )

        stat = path.stat()

        return ToolResult(
            tool=call.tool,
            success=True,
            metadata={
                "path": str(path.relative_to(self.root)),
                "size_bytes": stat.st_size,
                "is_file": path.is_file(),
                "is_dir": path.is_dir(),
            },
        )

    def _find(self, call: ToolCall) -> ToolResult:
        root = self._resolve(call.arguments.get("path", "."))
        pattern = call.arguments.get("pattern", "*")

        results = []

        for p in root.rglob("*"):
            rel = p.relative_to(self.root)

            if fnmatch.fnmatch(str(rel), pattern):
                results.append(str(rel))

            if len(results) >= MAX_FIND_RESULTS:
                break

        return ToolResult(
            tool=call.tool,
            success=True,
            stdout="\n".join(results),
            metadata={"matches": len(results)},
        )

    def _grep(self, call: ToolCall) -> ToolResult:
        root = self._resolve(call.arguments.get("path", "."))
        pattern = call.arguments.get("pattern", "")

        regex = re.compile(pattern)

        matches = []

        for file in root.rglob("*"):
            if not file.is_file():
                continue

            try:
                with Path.open(file, encoding="utf-8", errors="ignore") as f:
                    for i, line in enumerate(f, 1):
                        if regex.search(line):
                            rel = file.relative_to(self.root)
                            matches.append(f"{rel}:{i}:{line.strip()}")

                        if len(matches) >= MAX_GREP_RESULTS:
                            break
            except Exception:
                continue

        return ToolResult(
            tool=call.tool,
            success=True,
            stdout="\n".join(matches),
            metadata={"matches": len(matches)},
        )

    def _write(self, call: ToolCall) -> ToolResult:
        path = self._resolve(call.arguments.get("path", ""))

        path.parent.mkdir(parents=True, exist_ok=True)

        if path.exists() and not call.arguments.get("overwrite", False):
            return ToolResult(
                tool=call.tool, success=False, stderr="File already exists."
            )

        path.write_text(call.arguments.get("content", ""))

        return ToolResult(
            tool=call.tool,
            success=True,
            metadata={"path": str(path.relative_to(self.root))},
        )

    def _patch(self, call: ToolCall) -> ToolResult:
        path = self._resolve(call.arguments.get("path", ""))

        if not path.exists():
            return ToolResult(
                tool=call.tool,
                success=False,
                stderr="File does not exist.",
            )

        patch_text = call.arguments.get("patch", "")

        try:
            original = path.read_text()

            patched = apply_search_replace_patch(original, patch_text)

            path.write_text(patched)

            return ToolResult(
                tool=call.tool,
                success=True,
                metadata={"path": str(path)},
            )

        except Exception as e:
            return ToolResult(
                tool=call.tool,
                success=False,
                stderr=str(e),
            )
