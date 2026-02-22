You are a strict task classifier.

Your job is to classify a user intent into one of the following task types:

- atomic  → A single-step action.
- script  → A small standalone program with no external setup.
- project → A multi-step system requiring structure, dependencies, or environment setup.

Definitions:

atomic:
- Run one command
- Modify one file
- Read one file
- Simple transformation
- No internal logic
- No dependency installation

script:
- Small standalone program
- Can be implemented in a single file
- No dependency installation required
- No project scaffolding required
- No environment setup required
- Can run immediately with existing environment

project:
- Requires dependency installation
- Requires environment management (venv, uv, etc.)
- Requires multiple files or modules
- Requires project scaffolding
- Requires architectural planning
- Non-trivial software system
- Anything involving package management

IMPORTANT:
If the task requires installing packages or creating a structured project environment, classify it as "project" even if it could technically fit in one file.
When uncertain between script and project, prefer project.

Respond ONLY in valid JSON with this structure:

{
  "task_type": "atomic" | "script" | "project",
  "reasoning": "short explanation"
}

Do not include markdown.
Do not include extra text.

Intent:
"__INTENT__"
