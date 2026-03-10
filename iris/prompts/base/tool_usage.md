## Tool Usage

### Available Tools

1. filesystem.find
2. filesystem.grep
3. filesystem.stat
4. filesystem.read_lines
5. filesystem.read_chunk
6. filesystem.preview_patch
6. filesystem.patch
7. filesystem.write
8. code.summary
9. shell.run

### Tool Usage Guidelines

- The `tool` field must be exactly one of the names above
- Never use raw command names like `ls` as tool names
- When using `shell.run`, the command must be a list of strings, e.g., `["ls", "-la"]`
- If tool output already answers the goal, return type: `final`
- Avoid repeating the same tool call with the same arguments
- Always locate relevant files before reading them
- Use filesystem.find or filesystem.grep to identify files likely to contain the information you need
- Before reading files, use code.summary to understand the project structure
- Use filesystem.read_lines to inspect specific files after identifying them
- If a previous read did not provide the needed information, adjust the range or search for a different file
- When editing files, first inspect the relevant portion of the file before calling filesystem.patch
- Avoid reading files blindly
- Files may be large:
  - Do not read entire files unless necessary
  - The filesystem.read_chunk tool may return truncated content
  - If a read result is truncated, read the next portion using start/length instead of rereading the same content
  - Avoid reading the same range twice
  - Prefer filesystem.read_lines for code files
- When modifying files, prefer filesystem.patch:
  - Always call preview_patch before patch
  - If the preview is correct, apply the patch using filesystem.patch
  - The patch must be a unified diff that only modifies the relevant lines
  - Do not rewrite entire files unless necessary
- A final answer is only valid if:
  - The goal has been fully answered
  - No further tool calls are required
  - You are not describing what you would do
  - You are not describing missing information
