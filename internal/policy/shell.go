package policy

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// ShellMode controls how shell commands are authorised.
type ShellMode string

const (
	// ShellModeDeny disables the shell tool entirely.
	ShellModeDeny ShellMode = "deny"

	// ShellModeAllowlist only permits commands that match security.shell.allow.
	// Commands that cannot be analysed statically are never allowlisted.
	ShellModeAllowlist ShellMode = "allowlist"

	// ShellModeApproval runs allowlisted commands without prompting and asks
	// the configured approver for everything else. Without an approver, all
	// non-allowlisted commands fail closed.
	ShellModeApproval ShellMode = "approval"

	// ShellModeAllow runs every command except explicit deny rules. It must
	// be combined with security.external_sandbox = true because Iris cannot
	// confine what a shell command does.
	ShellModeAllow ShellMode = "allow"
)

// ShellOptions is the configurable shell policy. Zero values are replaced by
// safe defaults during policy construction.
type ShellOptions struct {
	// Mode selects the authorisation strategy (deny, allowlist, approval or
	// allow). Empty defaults to "approval".
	Mode ShellMode

	// Allow is a list of glob patterns for commands that do not need
	// approval. Patterns containing whitespace match the whole command
	// (or one command segment); patterns without whitespace match executable
	// names.
	Allow []string

	// Deny is a list of glob patterns that are always rejected, even in
	// approval or allow mode. Patterns are matched the same way as Allow.
	Deny []string

	// DisableDefaultDeny removes the built-in catastrophic-command deny list.
	// It is an explicit opt-out; user-supplied Deny rules still apply.
	DisableDefaultDeny bool

	// ApprovalHook is an optional command (argv, no shell) that is executed
	// to decide approval requests. Exit 0 allows, non-zero denies.
	ApprovalHook []string

	// Timeout bounds a single shell command.
	Timeout time.Duration

	// ApprovalTimeout bounds how long Iris waits for an approval decision.
	ApprovalTimeout time.Duration

	// MaxOutputBytes bounds the model-visible text returned by any tool.
	MaxOutputBytes int

	// MaxOutputLines bounds the number of model-visible lines returned by any
	// tool.
	MaxOutputLines int

	// MaxCaptureBytes bounds the full output stored separately for retrieval.
	MaxCaptureBytes int

	// TempDirTTL controls removal of ephemeral tool-output directories left
	// behind by Iris processes that exited abnormally.
	TempDirTTL time.Duration

	// ShellBinary overrides the shell used to execute commands. Empty means
	// bash when available, otherwise sh.
	ShellBinary string
}

// ShellPolicy is the normalised, immutable shell policy attached to a Policy.
type ShellPolicy struct {
	Mode               ShellMode
	Allow              []string
	Deny               []string
	DisableDefaultDeny bool
	ApprovalHook       []string
	Timeout            time.Duration
	ApprovalTimeout    time.Duration
	MaxOutputBytes     int
	MaxOutputLines     int
	MaxCaptureBytes    int
	TempDirTTL         time.Duration
	ShellBinary        string
}

// ParseShellMode normalises a configured shell mode. An empty value defaults
// to approval mode.
func ParseShellMode(value string) (ShellMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "approval", "ask":
		return ShellModeApproval, nil
	case "deny", "disabled", "off":
		return ShellModeDeny, nil
	case "allowlist", "allow-list":
		return ShellModeAllowlist, nil
	case "allow", "all", "yolo":
		return ShellModeAllow, nil
	default:
		return "", fmt.Errorf(
			"%w: unknown shell mode %q (want %q, %q, %q or %q)",
			ErrInvalidPolicy,
			value,
			ShellModeDeny,
			ShellModeAllowlist,
			ShellModeApproval,
			ShellModeAllow,
		)
	}
}

// CommandAnalysis is the result of statically inspecting a shell command.
type CommandAnalysis struct {
	// Command is the raw command that was analysed.
	Command string

	// Segments are the simple commands separated by shell control operators.
	Segments []CommandSegment

	// Programs lists the executable name of each segment.
	Programs []string

	// Opaque is true when the command contains constructs that the static
	// analyser cannot reason about (command substitution, sub-shells, shell
	// wrappers, heredocs, and so on). Opaque commands are never allowlisted.
	Opaque bool

	// OpaqueReason explains why the command is opaque.
	OpaqueReason string
}

// CommandSegment is one simple command within a shell pipeline or list.
type CommandSegment struct {
	Tokens  []string
	Program string
	Text    string
}

// AnalyzeCommand performs a conservative, best-effort parse of a shell
// command. It is a policy gate, not a shell. Anything it cannot understand is
// marked opaque so that it requires explicit approval.
func AnalyzeCommand(command string) CommandAnalysis {
	analysis := CommandAnalysis{Command: command}

	var tokens []string
	var word strings.Builder
	wordStarted := false
	var quote rune
	escaped := false
	redirectTarget := false

	flushWord := func() {
		if !wordStarted {
			return
		}
		token := word.String()
		word.Reset()
		wordStarted = false
		if redirectTarget {
			redirectTarget = false
			return
		}
		tokens = append(tokens, token)
	}

	flushSegment := func() {
		flushWord()
		if len(tokens) == 0 {
			return
		}
		segment := CommandSegment{
			Tokens:  append([]string(nil), tokens...),
			Program: programFromTokens(tokens),
			Text:    strings.Join(tokens, " "),
		}
		analysis.Segments = append(analysis.Segments, segment)
		if segment.Program != "" {
			analysis.Programs = append(analysis.Programs, segment.Program)
		}
		tokens = nil
	}

	markOpaque := func(reason string) {
		if !analysis.Opaque {
			analysis.Opaque = true
			analysis.OpaqueReason = reason
		}
	}

	runes := []rune(command)

	for i := 0; i < len(runes); i++ {
		r := runes[i]

		if escaped {
			word.WriteRune(r)
			wordStarted = true
			escaped = false
			continue
		}

		switch quote {
		case '\'':
			if r == '\'' {
				quote = 0
			} else {
				word.WriteRune(r)
				wordStarted = true
			}
			continue
		case '"':
			switch r {
			case '"':
				quote = 0
			case '\\':
				escaped = true
			default:
				if r == '`' {
					markOpaque("backtick command substitution")
					i = skipBacktick(runes, i+1)
					wordStarted = true
					continue
				}
				if r == '$' && i+1 < len(runes) && runes[i+1] == '(' {
					markOpaque("command substitution")
					i = skipBalancedParen(runes, i+1)
					word.WriteRune('$')
					wordStarted = true
					continue
				}
				if r == '$' && i+1 < len(runes) && runes[i+1] == '\'' {
					markOpaque("ANSI-C quoting")
				}
				word.WriteRune(r)
				wordStarted = true
			}
			continue
		}

		switch r {
		case '\\':
			escaped = true
			wordStarted = true
		case '\'':
			quote = '\''
			wordStarted = true
		case '"':
			quote = '"'
			wordStarted = true
		case ' ', '\t', '\r':
			flushWord()
		case '\n':
			flushSegment()
			redirectTarget = false
		case ';', '&', '|':
			// "&>" is a redirection, not a background separator.
			if r == '&' && i+1 < len(runes) && runes[i+1] == '>' {
				flushSegment()
				i++
				if i+1 < len(runes) && runes[i+1] == '>' {
					i++
				}
				redirectTarget = true
				continue
			}
			flushSegment()
			redirectTarget = false
		case '(':
			markOpaque("subshell or grouping")
			flushSegment()
		case ')':
			flushSegment()
		case '{', '}':
			markOpaque("brace expansion or grouping")
			flushSegment()
		case '`':
			markOpaque("backtick command substitution")
			i = skipBacktick(runes, i+1)
			wordStarted = true
		case '$':
			if i+1 < len(runes) && runes[i+1] == '(' {
				markOpaque("command substitution")
				i = skipBalancedParen(runes, i+1)
				word.WriteRune('$')
				wordStarted = true
				continue
			}
			if i+1 < len(runes) && runes[i+1] == '\'' {
				markOpaque("ANSI-C quoting")
			}
			word.WriteRune(r)
			wordStarted = true
		case '<', '>':
			if i+1 < len(runes) && runes[i+1] == '(' {
				markOpaque("process substitution")
				i = skipBalancedParen(runes, i+1)
				wordStarted = true
				continue
			}

			flushWord()
			// A leading file-descriptor number belongs to the redirection
			// (for example "2>" or "2>>"), not to the command.
			if len(tokens) > 0 && isAllDigits(tokens[len(tokens)-1]) {
				tokens = tokens[:len(tokens)-1]
			}
			flushSegment()

			if r == '<' && i+1 < len(runes) && runes[i+1] == '<' {
				markOpaque("here-document")
			}
			for i+1 < len(runes) && (runes[i+1] == '<' || runes[i+1] == '>' || runes[i+1] == '&') {
				i++
			}
			redirectTarget = true
		default:
			word.WriteRune(r)
			wordStarted = true
		}
	}

	flushSegment()

	if quote != 0 {
		markOpaque("unterminated quote")
	}
	if escaped {
		markOpaque("unterminated escape")
	}

	for _, segment := range analysis.Segments {
		if segment.Program == "" {
			continue
		}
		base := programBase(segment.Program)
		if opaquePrograms[base] {
			markOpaque(fmt.Sprintf("%q can execute arbitrary code", base))
		}
		if base == "find" && findExecutes(segment.Tokens) {
			markOpaque("find can execute or delete arbitrary files")
		}
	}

	return analysis
}

// AuthorizeShell applies the shell policy to a command and, when needed, asks
// the configured approver. It returns an error wrapping one of the policy
// sentinel errors if the command may not run.
func (p *Policy) AuthorizeShell(ctx context.Context, command, workdir string) (CommandAnalysis, error) {
	analysis := AnalyzeCommand(command)

	mode := p.Mode()
	shell := p.ShellOptions()

	if strings.TrimSpace(command) == "" {
		return analysis, fmt.Errorf("%w: command is empty", ErrCommandDenied)
	}

	if mode == ModeReadOnly {
		return analysis, fmt.Errorf(
			"%w: security.mode is %q; shell commands are not permitted because they can modify files outside the workspace",
			ErrShellDisabled,
			ModeReadOnly,
		)
	}

	if shell.Mode == ShellModeDeny {
		return analysis, fmt.Errorf(
			"%w: security.shell.mode is %q",
			ErrShellDisabled,
			ShellModeDeny,
		)
	}

	if pattern, denied := shell.deniedPattern(analysis); denied {
		return analysis, fmt.Errorf(
			"%w: command matches deny rule %q",
			ErrCommandDenied,
			pattern,
		)
	}

	if shell.Mode == ShellModeAllow {
		return analysis, nil
	}

	if shell.isAllowlisted(analysis) {
		return analysis, nil
	}

	reason := "command is not allowlisted"
	if analysis.Opaque {
		reason = "command uses constructs that cannot be safely analysed: " + analysis.OpaqueReason
	}

	if shell.Mode == ShellModeAllowlist {
		return analysis, fmt.Errorf(
			"%w: %s; add a narrow pattern to security.shell.allow or use security.shell.mode = %q",
			ErrCommandDenied,
			reason,
			ShellModeApproval,
		)
	}

	if workdir == "" {
		workdir = p.Workdir()
	}

	decision, err := p.requestApproval(ctx, ApprovalRequest{
		Kind:     "shell",
		Command:  command,
		Workdir:  workdir,
		Programs: analysis.Programs,
		Reason:   reason,
	})
	if err != nil {
		if errors.Is(err, ErrApprovalUnavailable) {
			return analysis, fmt.Errorf(
				"%w for command %q: no interactive approval is available and security.shell.approval_hook is not configured; refusing to run",
				ErrApprovalUnavailable,
				truncateForMessage(command, 160),
			)
		}
		return analysis, err
	}

	if !decision.Allow {
		reason := strings.TrimSpace(decision.Reason)
		if reason == "" {
			reason = "denied by approver"
		}
		return analysis, fmt.Errorf("%w: %s", ErrCommandDenied, reason)
	}

	return analysis, nil
}

// requestApproval invokes the configured approver with a hard timeout. The
// approver runs in a goroutine so that a broken implementation cannot hang
// the agent forever.
func (p *Policy) requestApproval(ctx context.Context, request ApprovalRequest) (ApprovalDecision, error) {
	approver := p.Approver()
	if approver == nil {
		return ApprovalDecision{}, ErrApprovalUnavailable
	}

	timeout := p.ShellOptions().ApprovalTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}

	if err := ctx.Err(); err != nil {
		return ApprovalDecision{}, err
	}

	approvalCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type result struct {
		decision ApprovalDecision
		err      error
	}

	results := make(chan result, 1)
	go func() {
		decision, err := approver.Approve(approvalCtx, request)
		results <- result{decision: decision, err: err}
	}()

	select {
	case res := <-results:
		if res.err != nil {
			return ApprovalDecision{}, res.err
		}
		return res.decision, nil
	case <-approvalCtx.Done():
		if errors.Is(ctx.Err(), context.Canceled) {
			return ApprovalDecision{}, ctx.Err()
		}
		return ApprovalDecision{}, fmt.Errorf("%w after %s", ErrApprovalTimeout, timeout)
	}
}

func (s ShellPolicy) deniedPattern(analysis CommandAnalysis) (string, bool) {
	for _, pattern := range s.Deny {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}

		// Match the raw command first. This catches rules that contain shell
		// metacharacters but no whitespace (for example ":(){*").
		if globMatch(pattern, analysis.Command) {
			return pattern, true
		}

		if strings.ContainsAny(pattern, " \t") {
			for _, segment := range analysis.Segments {
				if globMatch(pattern, segment.Text) {
					return pattern, true
				}

				// Also match against a normalised segment where the program
				// path is reduced to its basename. This ensures a rule such as
				// "rm -rf /*" also catches "/bin/rm -rf /".
				if segment.Program != "" {
					base := programBase(segment.Program)
					if base != segment.Program {
						normalized := strings.Replace(segment.Text, segment.Program, base, 1)
						if globMatch(pattern, normalized) {
							return pattern, true
						}
					}
				}
			}
			continue
		}

		for _, program := range analysis.Programs {
			if globMatch(pattern, program) || globMatch(pattern, programBase(program)) {
				return pattern, true
			}
		}
	}

	return "", false
}

func (s ShellPolicy) isAllowlisted(analysis CommandAnalysis) bool {
	if analysis.Opaque || len(s.Allow) == 0 {
		return false
	}

	programs := 0
	for _, segment := range analysis.Segments {
		if segment.Program == "" {
			continue
		}
		programs++

		allowed := false
		for _, pattern := range s.Allow {
			pattern = strings.TrimSpace(pattern)
			if pattern == "" {
				continue
			}

			if strings.ContainsAny(pattern, " \t") {
				if globMatch(pattern, analysis.Command) || globMatch(pattern, segment.Text) {
					allowed = true
					break
				}
				continue
			}

			if globMatch(pattern, segment.Program) || globMatch(pattern, programBase(segment.Program)) {
				allowed = true
				break
			}
		}

		if !allowed {
			return false
		}
	}

	return programs > 0
}

func normalizeShellOptions(options ShellOptions) (ShellPolicy, error) {
	mode, err := ParseShellMode(string(options.Mode))
	if err != nil {
		return ShellPolicy{}, err
	}

	const (
		maxAllowedOutputBytes = 4 << 20   // 4 MiB
		maxAllowedOutputLines = 200_000   // 200k lines
		maxAllowedCapture     = 100 << 20 // 100 MiB
	)

	deny := cleanPatterns(options.Deny)
	if !options.DisableDefaultDeny {
		deny = append(cleanPatterns(defaultDenyPatterns), deny...)
	}

	normalized := ShellPolicy{
		Mode:               mode,
		Allow:              cleanPatterns(options.Allow),
		Deny:               deny,
		DisableDefaultDeny: options.DisableDefaultDeny,
		ApprovalHook:       cleanArgv(options.ApprovalHook),
		Timeout:            options.Timeout,
		ApprovalTimeout:    options.ApprovalTimeout,
		MaxOutputBytes:     options.MaxOutputBytes,
		MaxOutputLines:     options.MaxOutputLines,
		MaxCaptureBytes:    options.MaxCaptureBytes,
		TempDirTTL:         options.TempDirTTL,
		ShellBinary:        strings.TrimSpace(options.ShellBinary),
	}

	if normalized.Timeout <= 0 {
		normalized.Timeout = 2 * time.Minute
	}
	if normalized.ApprovalTimeout <= 0 {
		normalized.ApprovalTimeout = 5 * time.Minute
	}
	if normalized.MaxOutputBytes <= 0 {
		normalized.MaxOutputBytes = 25 * 1024
	}
	if normalized.MaxOutputLines <= 0 {
		normalized.MaxOutputLines = 1000
	}
	if normalized.MaxOutputBytes > maxAllowedOutputBytes {
		return ShellPolicy{}, fmt.Errorf(
			"%w: security.shell.max_output_bytes must be <= %d",
			ErrInvalidPolicy,
			maxAllowedOutputBytes,
		)
	}
	if normalized.MaxOutputLines > maxAllowedOutputLines {
		return ShellPolicy{}, fmt.Errorf(
			"%w: security.shell.max_output_lines must be <= %d",
			ErrInvalidPolicy,
			maxAllowedOutputLines,
		)
	}
	if normalized.MaxCaptureBytes <= 0 {
		normalized.MaxCaptureBytes = 10 * 1024 * 1024
	}
	if normalized.MaxCaptureBytes < normalized.MaxOutputBytes {
		normalized.MaxCaptureBytes = normalized.MaxOutputBytes
	}
	if normalized.MaxCaptureBytes > maxAllowedCapture {
		return ShellPolicy{}, fmt.Errorf(
			"%w: security.shell.max_capture_bytes must be <= %d",
			ErrInvalidPolicy,
			maxAllowedCapture,
		)
	}
	if normalized.TempDirTTL <= 0 {
		normalized.TempDirTTL = 24 * time.Hour
	}
	if normalized.ShellBinary == "" {
		normalized.ShellBinary = defaultShellBinary()
	}

	normalized.Deny = dedupeStrings(normalized.Deny)

	return normalized, nil
}

func defaultShellBinary() string {
	for _, candidate := range []string{"bash", "sh"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	return "sh"
}

func cleanPatterns(patterns []string) []string {
	cleaned := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern != "" {
			cleaned = append(cleaned, pattern)
		}
	}
	return cleaned
}

func cleanArgv(argv []string) []string {
	cleaned := make([]string, 0, len(argv))
	for _, arg := range argv {
		arg = strings.TrimSpace(arg)
		if arg != "" {
			cleaned = append(cleaned, arg)
		}
	}
	return cleaned
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

// skipBalancedParen returns the index of the ')' matching the '(' at start.
// It understands basic quoting and nesting so that command substitution
// contents are not mistaken for separate commands.
func skipBalancedParen(runes []rune, start int) int {
	if start < 0 || start >= len(runes) || runes[start] != '(' {
		return start
	}

	depth := 0
	for i := start; i < len(runes); i++ {
		switch runes[i] {
		case '\\':
			i++
		case '\'':
			i++
			for i < len(runes) && runes[i] != '\'' {
				i++
			}
		case '"':
			i++
			for i < len(runes) {
				if runes[i] == '\\' {
					i += 2
					continue
				}
				if runes[i] == '"' {
					break
				}
				i++
			}
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}

	return len(runes) - 1
}

// skipBacktick returns the index of the backtick closing a substitution that
// starts at start.
func skipBacktick(runes []rune, start int) int {
	for i := start; i < len(runes); i++ {
		if runes[i] == '\\' {
			i++
			continue
		}
		if runes[i] == '`' {
			return i
		}
	}
	return len(runes) - 1
}

func programFromTokens(tokens []string) string {
	for _, token := range tokens {
		if token == "" || isAssignment(token) {
			continue
		}
		return token
	}
	return ""
}

func isAssignment(token string) bool {
	index := strings.IndexByte(token, '=')
	if index <= 0 {
		return false
	}

	for i, r := range token[:index] {
		if i == 0 {
			if r != '_' && !unicode.IsLetter(r) {
				return false
			}
			continue
		}
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}

	return true
}

func programBase(program string) string {
	program = strings.TrimSpace(program)
	if index := strings.LastIndexAny(program, `/\`); index >= 0 {
		program = program[index+1:]
	}
	return program
}

func findExecutes(tokens []string) bool {
	for _, token := range tokens {
		switch token {
		case "-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprint0", "-fls":
			return true
		}
		if strings.HasPrefix(token, "-exec") || strings.HasPrefix(token, "-ok") {
			return true
		}
	}
	return false
}

func isAllDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// globMatch matches a shell-style glob where '*' matches any sequence
// (including separators) and '?' matches a single character.
func globMatch(pattern, value string) bool {
	var builder strings.Builder
	builder.WriteString("(?s)^")
	for _, r := range pattern {
		switch r {
		case '*':
			builder.WriteString(".*")
		case '?':
			builder.WriteString(".")
		default:
			builder.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	builder.WriteString("$")

	compiled, err := regexp.Compile(builder.String())
	if err != nil {
		return false
	}
	return compiled.MatchString(value)
}

func truncateForMessage(value string, max int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	if max <= 1 {
		return "…"
	}
	return string(runes[:max-1]) + "…"
}

// opaquePrograms are executables that can run arbitrary code or another
// program. A command containing one of these is never allowlisted.
var opaquePrograms = map[string]bool{
	"eval":       true,
	"exec":       true,
	"command":    true,
	"builtin":    true,
	"source":     true,
	".":          true,
	"bash":       true,
	"sh":         true,
	"zsh":        true,
	"dash":       true,
	"ksh":        true,
	"fish":       true,
	"csh":        true,
	"tcsh":       true,
	"env":        true,
	"xargs":      true,
	"sudo":       true,
	"doas":       true,
	"su":         true,
	"runuser":    true,
	"nohup":      true,
	"setsid":     true,
	"time":       true,
	"nice":       true,
	"stdbuf":     true,
	"timeout":    true,
	"watch":      true,
	"parallel":   true,
	"script":     true,
	"expect":     true,
	"ssh":        true,
	"docker":     true,
	"podman":     true,
	"chroot":     true,
	"unshare":    true,
	"nsenter":    true,
	"python":     true,
	"python2":    true,
	"python3":    true,
	"perl":       true,
	"ruby":       true,
	"node":       true,
	"nodejs":     true,
	"deno":       true,
	"bun":        true,
	"php":        true,
	"lua":        true,
	"luajit":     true,
	"Rscript":    true,
	"julia":      true,
	"tclsh":      true,
	"wish":       true,
	"guile":      true,
	"racket":     true,
	"swift":      true,
	"java":       true,
	"javac":      true,
	"dotnet":     true,
	"mono":       true,
	"pwsh":       true,
	"powershell": true,
	"osascript":  true,
	"busybox":    true,
	"vim":        true,
	"nvim":       true,
	"vi":         true,
	"less":       true,
	"more":       true,
	"awk":        true,
	"gawk":       true,
	"mawk":       true,
	"nawk":       true,
	"sed":        true,
	"make":       true,
	"gmake":      true,
}

// defaultDenyPatterns are always applied, even when the user supplies their
// own deny list. They are defence in depth, not a sandbox.
var defaultDenyPatterns = []string{
	"sudo",
	"doas",
	"su",
	"runuser",
	"mkfs*",
	"fdisk",
	"parted",
	"shutdown",
	"reboot",
	"halt",
	"poweroff",
	"rm -rf /*",
	"rm -fr /*",
	"rm -rf ~*",
	"rm -fr ~*",
	":(){*",
}
