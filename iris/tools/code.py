from pathlib import Path
import re

from iris.executor.models.tool_call import ToolCall, ToolResult
from iris.tools.base import Tool
from iris.utils.logger import logger

MAX_FILES = 200


class CodeTool(Tool):
    def __init__(self, root: str):
        self.root = Path(root).resolve()

        assert self.root and Path(self.root).exists(), (
            "Workspace directory does not exist"
        )
        assert Path(self.root).is_dir(), "Workspace must be a directory"

    def execute(self, call: ToolCall) -> ToolResult:
        if call.tool != "code.summary":
            return ToolResult(
                tool=call.tool, success=False, stderr="Unsupported code operation."
            )

        files = []

        for path in self.root.rglob("*"):
            if not path.is_file():
                continue

            if path.suffix not in [".py", ".js", ".ts", ".go", ".rs", ".java"]:
                continue

            symbols = []

            try:
                text = path.read_text(errors="ignore")

                symbols += re.findall(r"class\s+([A-Za-z0-9_]+)", text)
                symbols += re.findall(r"def\s+([A-Za-z0-9_]+)", text)
                symbols += re.findall(r"function\s+([A-Za-z0-9_]+)", text)

            except Exception:
                continue

            files.append(
                {
                    "path": str(path.relative_to(self.root)),
                    "symbols": symbols[:10],
                }
            )

            if len(files) >= MAX_FILES:
                break

        logger.debug(f"Code summary generated with {len(files)} files.")

        return ToolResult(
            tool=call.tool,
            success=True,
            metadata={
                "files": files,
                "total_files": len(files),
            },
        )
