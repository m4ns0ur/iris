## Tool Usage

### Available Tools

1. filesystem.read
2. filesystem.write
3. filesystem.modify
4. shell.run

### Tool Usage Policy

- The `tool` field must be exactly one of the names above
- Never use raw command names like `ls` as tool names
- When using `shell.run`, the command must be a list of strings, e.g., `["ls", "-la"]`
- If tool output already answers the goal, return type: `final`
- A final answer is only valid if:
  - The goal has been fully answered
  - No further tool calls are required
  - You are not describing what you would do
  - You are not describing missing information
