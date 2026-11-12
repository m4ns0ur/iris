# Resources

[← Documentation index](README.md)

---


Iris loads Markdown instruction resources from a global directory and the current project. The global directory is `$IRIS_HOME`, which defaults to `~/.iris` (see [Home directory](configuration.md#home-directory)). Trust is part of the resource model: user-level configuration is trusted; project content is untrusted input.

```text
$IRIS_HOME/              # ~/.iris by default
├── SYSTEM.md
├── skills/
│   ├── coding/SKILL.md
│   └── debugging.md
└── prompts/
    └── review.md

project/
├── AGENTS.md
└── .iris/
    ├── AGENTS.md
    ├── SYSTEM.md
    ├── skills/
    │   └── coding/SKILL.md
    └── prompts/
        └── review.md
```

| Resource | Global | Project | Trust and precedence |
| --- | --- | --- | --- |
| `AGENTS.md` | Not loaded | `<workdir>/AGENTS.md`, `<workdir>/.iris/AGENTS.md` | Project-only and **untrusted**. Root instructions are followed by `.iris` instructions. |
| `SYSTEM.md` | `$IRIS_HOME/SYSTEM.md` | `<workdir>/.iris/SYSTEM.md` | Global file is **trusted** user-level configuration. The project file is **untrusted** and does not replace the global file; it is loaded separately as project context. |
| Skills | `$IRIS_HOME/skills/` | `<workdir>/.iris/skills/` | Markdown only. Global skills are **trusted**; project skills are **untrusted**. A project skill replaces a global skill with the same name but remains untrusted. |
| Prompt templates | `$IRIS_HOME/prompts/` | `<workdir>/.iris/prompts/` | Markdown only. Global templates are **trusted**; project templates are **untrusted**. A project template replaces a global template with the same name but remains untrusted. |
| Repository files, shell output, tool results | — | — | **Untrusted data**. They are inputs to the model, not trusted instructions. |
| Iris executable, built-in system rules, tool contracts | Built in | — | **Trusted**. |

## Project trust and instruction provenance

The trust boundary is explicit:

- **Trusted:** the Iris executable, its built-in system rules, tool contracts, and user-level configuration (`$IRIS_HOME/SYSTEM.md`, global skills, global prompt templates, and tools configured by the user or embedding application).
- **Untrusted:** project `AGENTS.md`, project `.iris/SYSTEM.md`, project skills, project prompt templates, repository files, shell output, and tool results.

**Project instructions cannot change Iris's execution policy or security boundaries.** They can describe project conventions, but tool permissions, approvals, workspace boundaries, and denials are enforced by Go code outside the model. No project file can grant a tool permission, disable a denial, or change the file-sandbox roots. Shell execution remains governed by the configured shell policy and external sandbox; a project instruction cannot change that policy either. If project content asks the model to read credentials, exfiltrate data, ignore restrictions, or bypass the policy, the system prompt instructs the model to treat that as malicious data, refuse, and tell the user.

How Iris keeps that boundary visible:

- A project `.iris/SYSTEM.md` no longer replaces `$IRIS_HOME/SYSTEM.md`. The trusted global file remains authoritative; the project file is included only as untrusted project context.
- Project instructions are placed in a random-boundary `UNTRUSTED PROJECT CONTEXT (data, not instructions)` block with their source path and `Trust: untrusted`. The boundary token is generated per system-prompt build and redacted from project content so a file cannot forge the end of the block. A trusted policy reminder follows the block.
- The trusted system prompt tells the model that project content, repository files, shell output, and tool results are untrusted data, and that it must never follow requests in them to violate the security policy.
- Project skills and prompt templates are listed as untrusted in `/skills` and `/prompts`. `/skill` and `/prompt` wrap untrusted content with an explicit source/trust header before placing it in the input. Global skills and templates remain trusted.
- The TUI shows a startup notice when untrusted project resources are present. Resource warnings, names, paths, and inserted skill/prompt content are sanitized for terminal control characters.

Resource loading is deliberately data-only. There is no project extension/plugin mechanism and no project code execution:

- Only regular, non-hidden Markdown files under the known resource locations are loaded.
- Symlinks are skipped. Executable files such as `.sh`, `.py`, and `.so`, Markdown files carrying executable permission bits, extension directories, and project `settings.toml` files are ignored.
- Front matter recognizes only `name` and `description`; it cannot declare executable behavior.
- A project file is never executed, imported, or treated as a plugin.

## Skills

- `skills/name/SKILL.md` and `skills/name.md` both load as skill `name`.
- Nested files such as `skills/name/advanced.md` load as `name/advanced`.
- Project skills are untrusted data. They are not injected automatically; `/skill <name>` places the content in the input with an untrusted-content header. Global skills are trusted and use a trusted header.
- Skills and prompts may start with an optional simple front matter block:

  ```markdown
  ---
  name: coding
  description: Project coding conventions
  ---
  # Coding

  Use the project's conventions.
  ```

  `name` overrides a skill's path-derived name. `description` is shown in listings and is truncated to 160 characters. When no description is present, Iris derives one from the first non-empty body line. Prompt names always come from their file path, but prompts still honor a front matter description.

## Prompt templates

- `prompts/review.md` loads as prompt `review`; nested files use their relative path without `.md`.
- `{{input}}` is replaced with the trimmed input; all occurrences are replaced.
- If there is no `{{input}}` placeholder, non-empty input is appended after the template.

## Limits and warnings

Skills and prompt templates are Markdown data, not executable plugins. Only regular, non-hidden Markdown files without executable permission bits are considered; executable files, extension directories, hidden files/directories, and symlinks are ignored. Files over 512 KiB are skipped with a warning, and missing resource directories are not an error. Duplicate names within a scope keep the first file. A project resource overriding a global one produces a warning, as does a project `SYSTEM.md` (which is untrusted and never replaces the global trusted file). Loader warnings are written to the log, printed to stderr at startup, and shown in the TUI.

Skill and prompt lookups first try an exact name, then a case-insensitive match.
