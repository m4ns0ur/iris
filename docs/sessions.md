# Sessions

[← Documentation index](README.md)

---


Sessions are persisted as one append-only JSONL journal per conversation under `$IRIS_HOME/sessions` (`~/.iris/sessions` by default). Override the location with:

```toml
[session]
dir = '/path/to/sessions'
```

The session directory is created with `0700` permissions, session files with `0600`, and per-session lock files with `0600`. A session header and full rewrites are written atomically via a temp file, file sync, rename, and directory sync. Individual entries are appended and fsynced.

Logs follow the same per-session split. While a session is active, its debug records are appended to `$IRIS_HOME/logs/<session-id>.log`; resuming or forking a session opens its own log file. Records written outside a session go to `$IRIS_HOME/logs/iris.log`. The log location follows the Iris home directory and is independent of `session.dir`.

## Durability and crash recovery

The runtime records the transcript at three boundaries:

| Boundary | Persistence point |
| --- | --- |
| User message | Before the model request that consumes it, including delivered steering and follow-up messages. |
| Assistant message | After the complete response is validated, and before any of its tool calls execute. |
| Tool result | Immediately after the tool returns, is rejected, or is skipped, before the next tool or model call. |

Recording failures abort the run instead of continuing with unrecorded work:

- A failed user-message write stops the run before the model call.
- A failed assistant-message write stops the run before any tool call executes.
- A failed tool-result write stops the run after the tool has run. The assistant tool call remains durable without a result, and recovery identifies it as interrupted.

Crash recovery is performed by `Acquire` under the exclusive writer lease:

- A partially written final JSONL record is ignored when loading; the valid prefix is used.
- The file is repaired (partial tail removed, schema upgraded) before new entries are appended.
- Every assistant tool call without a recorded result gets a synthetic `tool` entry with `status: "interrupted"` and `recovered: true`.
- Its content is a structured tool error with `code: "tool_execution_interrupted"`, `interrupted: true`, `executed: true`, and `retryable: false`. Iris cannot know whether the tool ran, so the model is told to verify side effects before retrying.
- Damage in the *middle* of a journal is an error rather than a silent truncation.

Only complete model responses are committed. A failed, disconnected, or retried request contributes no assistant entry, so an interrupted response cannot be mistaken for completed work in a resumed session. The TUI shows a notice for each recovered interrupted tool call.

## Transcript versus model context

Sessions maintain two related views:

- **Transcript history** (`Session.Entries`): everything the user and agent said or did, including tool calls, tool results, errors, and recovered interruptions. It is never compacted or replaced.
- **Model context** (`Session.History`): the messages sent to the model. Orphan tool results are omitted, dangling tool calls are represented by interrupted results, and the context manager applies compaction to a prompt view only.

Context compaction therefore changes what the model sees for a request without deleting or rewriting the durable transcript. A session resumed later still contains the original messages; it does not contain compaction summaries as conversation entries.

## Schema and journal format

The format has an explicit schema version. The current version is **2**; unversioned and version 1 files are accepted and upgraded on `Acquire`. A newer version fails with `session.ErrUnsupportedSessionVersion`.

The first record is the versioned session header. Entries and checkpoints follow. Each append writes an entry plus a checkpoint carrying the new revision, timestamp, and aggregate usage:

```json
{"type":"session","version":2,"session":{"id":"s-...","revision":3,"created_at":"...","updated_at":"...","model":"...","model_metadata":{...},"workspace":"...","usage":{...}}}
{"type":"entry","entry":{"id":"e-...","session_id":"s-...","parent_id":"...","kind":"user","created_at":"...","content":"..."}}
{"type":"entry","entry":{"id":"e-...","session_id":"s-...","parent_id":"...","kind":"assistant","created_at":"...","content":"...","reasoning_content":"...","tool_calls":[...],"usage":{...},"model":"..."}}
{"type":"entry","entry":{"id":"e-...","session_id":"s-...","parent_id":"...","kind":"tool","created_at":"...","content":"...","tool_call_id":"...","tool_name":"..."}}
{"type":"checkpoint","checkpoint":{"revision":4,"updated_at":"...","usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}}
```

An interrupted tool entry adds:

```json
{"kind":"tool","status":"interrupted","recovered":true,"tool_call_id":"call_1","tool_name":"shell","content":"{\"ok\":false,\"error\":{\"code\":\"tool_execution_interrupted\",\"...\":\"...\"}}"}
```

`Save` rewrites the complete session atomically with the next header revision; `Append` uses the journal path. Unknown record types within a supported version are ignored for forward compatibility. The header's `revision` and checkpoint revisions are monotonic and used for lost-update detection.

Session metadata includes the ID, timestamps, revision, model, model metadata (provider, base URL, model context size, effective context window, configured reserve tokens, non-zero sampling values), workspace, parent/fork information, and aggregate token usage (summed from recorded assistant turns and compaction summaries).

Entries have IDs and `parent_id` links. Message history is reconstructed from these entries when a session is resumed. System prompts are not persisted; the agent adds its current system prompt when history is loaded. Resuming and forking restore conversation history and metadata; new turns use the provider and model configured for the current Iris launch.

Steering instructions and follow-ups that were delivered during a run are ordinary user messages, so they are persisted as `user` entries before the next model request. Instructions that were still queued when the run ended are not part of the conversation and are not persisted.

Session IDs use letters, digits, `-`, and `_`, up to 128 characters.

## Concurrent writers

A session has one writer at a time:

- `Acquire` holds an exclusive writer lease on `<session>.lock`. On Unix it uses a non-blocking `flock`; a process-local guard also covers multiple managers in the same process. Another writer receives `session.ErrSessionLocked`.
- The OS releases the lock automatically when a process crashes; stale Unix lock files are harmless. The non-Unix fallback uses an exclusive lock file and may require removing a stale `.lock` file after a crash.
- `Save` also takes a transient lease when the caller does not already hold one, and compares the session `revision` with the file. A stale writer receives `session.ErrSessionConflict` instead of overwriting newer entries.
- Read-only `Open`, `List`, `Latest`, and exports do not require the writer lease. A session actively being written can still be read; a partial final record is ignored.

## Tool-output files

Full tool output is stored under `<session-dir>/tool-output/` with directory `0700` and files `0600`. The directory is shared by sessions in the same store so a forked or resumed transcript can still read the paths recorded in its tool results. It is not listed as a session and is never loaded as instruction data. Tool-output files can contain sensitive command output and are not deleted when an agent closes; remove them with the session store when no longer needed.

The client and TUI call `session.ToolOutputProvider.ToolOutputDir` and `agent.ToolOutputAgent.SetToolOutputDir` when attaching a session. Standalone agents, or managers that do not implement the optional provider, keep full output in an ephemeral private directory that is removed on `Close`.

## Exports

`/export` and `session.export` write Markdown to `<session-dir>/exports/<session-id>.md` (directory `0700`, file `0600`, atomic write). The export includes format version, revision, session metadata, token totals, model metadata, and each entry with reasoning sections, tool calls, tool results, and interrupted status.

## Client and RPC session behavior

- `print`, `jsonl`, and `rpc` create a session at startup unless an initial session is supplied by an embedding. The client acquires its writer lease and records user messages before execution.
- Delivered steering instructions and follow-ups are saved as user entries; queued instructions that were never delivered are not.
- `session.new` and `session.resume` persist the current session before switching and acquire the new session's lease. `session.resume` also repairs a session left behind by a crash.
- `session.fork` creates a child session containing entries through the selected user message, records `parent_session_id` and `forked_from_entry_id`, and acquires the child's lease.
- The client and TUI attach the session tool-output directory to the agent when switching sessions, so full tool output recorded in the transcript remains readable after resume or fork.
- Mutating session methods return busy errors during a run; see [RPC](cli.md#rpc). Operations on a session locked by another process return `session.ErrSessionLocked`.

## `session.Manager`

The public `session` package defines the storage abstraction used by the client and TUI:

```go
type Manager interface {
    Create(options CreateOptions) (*Session, error)
    Save(session *Session) error
    Open(id string) (*Session, error)
    Latest() (*Session, error)
    List() ([]Summary, error)
    Fork(sessionID, fromEntryID string) (*Session, error)
    ExportMarkdown(session *Session) (string, error)
    Acquire(session *Session) (Lease, error)
    Append(session *Session, entry Entry) (Entry, error)
}

type Lease interface {
    Release() error
}

// Optional: provides a persistent tool-output directory. The client/TUI use
// it when available; otherwise the agent uses ephemeral output storage.
type ToolOutputProvider interface {
    ToolOutputDir(sessionID string) (string, error)
}
```

`session.NewJSONL(dir)` returns the default JSONL implementation. `Create` builds an unsaved session; `Acquire` takes the writer lease and repairs recoverable damage; `Append` durably journals one entry and requires the lease returned by `Acquire`; `Save` publishes a complete rewrite. A `Lease` is any value with `Release() error`; `Lease.Release` relinquishes the writer lease. Forking validates that the target entry exists and is a user message. Custom `Manager` implementations must satisfy the full interface, including `Acquire` and `Append`; the JSONL manager is the reference implementation. `JSONLManager` also implements `ToolOutputProvider`.
