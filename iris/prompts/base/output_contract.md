## Output Contract

Respond only with valid JSON in the following structure:

If tool required:
```
{
  "type": "tool",
  "call": {
    "tool": "...",
    "arguments": { ... }
  }
}
```
If finished:
{{ json_contract }}
- You must output only valid JSON
- Do not include explanations, markdown, or code fences
- The first character must be { and the last must be }
- Do NOT add extra fields
- Do NOT rename fields
- All string fields must contain literal values only
- The `tool` field must be exactly one of the available tools
- If tool output already answers the goal, return type: `final`
