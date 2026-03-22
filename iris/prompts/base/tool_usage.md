## Tool Usage

### Available Tools

1. `filesystem.find`
  - Description: Search for files matching a pattern
  - Arguments: `path`, `pattern`

2. `filesystem.grep`
  - Description: Search for a pattern in a file
  - Arguments: `path`, `pattern`

3. `filesystem.stat`
  - Description: Get file statistics
  - Arguments: `path`

4. `filesystem.read_lines`
  - Description: Read specific lines from a file
  - Arguments: `path`, `start`, `num`
  - Constraints:
    - Avoid reading the same lines twice

5. `filesystem.read_chunk`
  - Description: Read a chunk of a file
  - Arguments: `path`, `start`
  - Constraints:
    - Avoid reading the same range twice
    - Always locate relevant files before reading them
    - If a previous read did not provide the needed information, adjust the range or search for a different file

6. `filesystem.patch`
  - Description: Apply a patch to a file using SEARCH/REPLACE blocks
  - Arguments: `path`, `patch`
  - Patch Format:
    ```
    <<<<<<< SEARCH
    exact original text
    =======
    replacement text
    >>>>>>> REPLACE
    ```
  - Example:
    ```
    <<<<<<< SEARCH
    PATTERN="$1"
    POSTFIX="$2"
    =======
    PATTERN="$1"
    EXTENSION="$2"
    >>>>>>> REPLACE
    ```
  - Constraints:
    - The SEARCH block must match the file content exactly
    - The SEARCH block must appear exactly once in the file
    - Include enough surrounding context to uniquely identify the change
    - Multiple SEARCH/REPLACE blocks may be used
    - Do not modify unrelated lines
    - Inspect the file before patching
    - Do not rewrite entire files unless necessary

7. `filesystem.write`
  - Description: Write content to a file
  - Arguments: `path`, `content`

8. `code.summary`
  - Description: Get a summary of the codebase
  - Arguments: `path`

9. `bash.run`
  - Description: Run a Bash command
  - Arguments: `command` (list of strings)

### Tool Usage Constraints

- Always check "Last 5 Tool Usage" section to avoid repeating the same tool call with the same arguments.
- Always check that file doesn't exist before `filesystem.write`. If file exists, use `filesystem.patch` to edit.
- Prefer `filesystem.read_lines` for code files.
- A final answer is only valid if:
  - The goal has been fully answered.
  - No further tool calls are required.
  - You are not describing what you would do.
  - You are not describing missing information.
