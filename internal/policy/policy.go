// Package policy implements Iris' execution boundary.
//
// It provides two independent controls:
//
//   - A filesystem sandbox for the built-in file tools. Paths are resolved
//     (including symlinks) and must remain inside the configured workspace or
//     explicitly allowed roots.
//   - A shell execution policy with allow/deny rules, configurable approval
//     hooks, timeouts, and output limits.
//
// The shell policy is not an OS sandbox. A permitted shell command can do
// anything the Iris process can do. For a hard security boundary, run Iris
// inside a container or VM and acknowledge that with
// security.external_sandbox = true. Iris surfaces this requirement in its
// startup warnings and system prompt instead of pretending its command
// parsing is a sandbox.
package policy

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// Options configures a Policy.
type Options struct {
	// Mode selects the filesystem policy. Empty defaults to workspace-write.
	Mode Mode

	// ReadPaths are extra paths (files or directories) the file tools may
	// read outside the workspace.
	ReadPaths []string

	// WritePaths are extra paths the file tools may write outside the
	// workspace. Write roots are also readable.
	WritePaths []string

	// Shell configures command execution.
	Shell ShellOptions

	// Approver decides commands that are not allowlisted. It takes precedence
	// over Shell.ApprovalHook. A nil approver means approval-required commands
	// fail closed.
	Approver Approver

	// ExternalSandbox acknowledges that Iris is running inside a container or
	// VM. It is required for ShellModeAllow and suppresses the unsandboxed
	// shell warning.
	ExternalSandbox bool
}

// Policy is the immutable (apart from the approver and extra read roots)
// execution boundary used by the tools.
type Policy struct {
	mu sync.RWMutex

	mode            Mode
	root            string
	readRoots       []string
	writeRoots      []string
	shell           ShellPolicy
	approver        Approver
	externalSandbox bool
}

// New validates the options and resolves the workspace root. The root must
// exist and be a directory.
func New(workdir string, options Options) (*Policy, error) {
	if strings.TrimSpace(workdir) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("%w: cannot determine working directory: %v", ErrInvalidPolicy, err)
		}
		workdir = cwd
	}

	root, err := canonicalize(workdir)
	if err != nil {
		return nil, fmt.Errorf("%w: workspace %q: %v", ErrInvalidPolicy, workdir, err)
	}

	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("%w: workspace %q: %v", ErrInvalidPolicy, workdir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: workspace %q is not a directory", ErrInvalidPolicy, workdir)
	}

	mode, err := ParseMode(string(options.Mode))
	if err != nil {
		return nil, err
	}

	shell, err := normalizeShellOptions(options.Shell)
	if err != nil {
		return nil, err
	}

	if shell.Mode == ShellModeAllow && mode != ModeReadOnly && !options.ExternalSandbox {
		return nil, fmt.Errorf(
			"%w: security.shell.mode = %q permits every command except deny rules and is not an OS sandbox; "+
				"only use it inside a container or VM and set security.external_sandbox = true to acknowledge that",
			ErrInvalidPolicy,
			ShellModeAllow,
		)
	}

	p := &Policy{
		mode:            mode,
		root:            root,
		readRoots:       []string{root},
		writeRoots:      []string{root},
		shell:           shell,
		externalSandbox: options.ExternalSandbox,
	}

	for _, path := range options.ReadPaths {
		resolved, err := resolveExtraRoot(path)
		if err != nil {
			return nil, fmt.Errorf("%w: security.read_paths: %v", ErrInvalidPolicy, err)
		}
		p.readRoots = appendUnique(p.readRoots, resolved)
	}

	for _, path := range options.WritePaths {
		resolved, err := resolveExtraRoot(path)
		if err != nil {
			return nil, fmt.Errorf("%w: security.write_paths: %v", ErrInvalidPolicy, err)
		}
		p.writeRoots = appendUnique(p.writeRoots, resolved)
		p.readRoots = appendUnique(p.readRoots, resolved)
	}

	approver := options.Approver
	if approver == nil && len(shell.ApprovalHook) > 0 {
		approver = &HookApprover{
			Command: shell.ApprovalHook,
			Timeout: shell.ApprovalTimeout,
		}
	}
	p.approver = approver

	return p, nil
}

// Mode returns the configured filesystem mode.
func (p *Policy) Mode() Mode {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.mode
}

// Root returns the canonical workspace root.
func (p *Policy) Root() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.root
}

// Workdir returns the canonical working directory for tools and prompts.
func (p *Policy) Workdir() string {
	return p.Root()
}

// ExternalSandbox reports whether the operator acknowledged an external
// sandbox.
func (p *Policy) ExternalSandbox() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.externalSandbox
}

// ShellOptions returns a copy of the normalised shell policy.
func (p *Policy) ShellOptions() ShellPolicy {
	p.mu.RLock()
	defer p.mu.RUnlock()

	shell := p.shell
	shell.Allow = append([]string(nil), p.shell.Allow...)
	shell.Deny = append([]string(nil), p.shell.Deny...)
	shell.ApprovalHook = append([]string(nil), p.shell.ApprovalHook...)
	return shell
}

// Approver returns the configured approver, if any.
func (p *Policy) Approver() Approver {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.approver
}

// SetApprover installs an approval mechanism. It is used by the TUI to wire
// its interactive prompt, and by embedding applications that provide their own
// approval UI.
func (p *Policy) SetApprover(approver Approver) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.approver = approver
}

// AddReadRoot grants the file tools read-only access to an additional
// directory. It is used for Iris' own temp directories (shell output files)
// and must be called before tool execution starts.
func (p *Policy) AddReadRoot(path string) error {
	resolved, err := canonicalize(path)
	if err != nil {
		return err
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", path)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.readRoots = appendUnique(p.readRoots, resolved)
	return nil
}

// Warnings describes security limitations that the caller should surface to
// the user and the model.
func (p *Policy) Warnings() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var warnings []string

	if p.mode == ModeDangerFullAccess {
		warnings = append(
			warnings,
			"security.mode is danger-full-access: file tools can read and write anywhere the Iris process can",
		)
	}

	shellActive := p.mode != ModeReadOnly && p.shell.Mode != ShellModeDeny

	if shellActive && !p.externalSandbox {
		warnings = append(
			warnings,
			"shell commands are not OS-sandboxed: approval rules and file path restrictions do not confine what a permitted command does. "+
				"Run Iris inside a container or VM, then set security.external_sandbox = true",
		)
	}

	if shellActive && p.shell.Mode == ShellModeAllow {
		warnings = append(
			warnings,
			"security.shell.mode is allow: every command that does not match a deny rule may run without approval",
		)
	}

	if shellActive && p.shell.DisableDefaultDeny {
		warnings = append(
			warnings,
			"security.shell.disable_default_deny is true: the built-in catastrophic-command deny list is disabled",
		)
	}

	return warnings
}

// Summary is a compact description of the active security posture.
func (p *Policy) Summary() string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	external := "not acknowledged"
	if p.externalSandbox {
		external = "acknowledged"
	}

	return fmt.Sprintf(
		"files=%s workspace=%s shell=%s external_sandbox=%s",
		p.mode,
		p.root,
		p.shell.Mode,
		external,
	)
}

func resolveExtraRoot(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("empty path")
	}

	resolved, err := canonicalize(path)
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(resolved); err != nil {
		return "", err
	}

	return resolved, nil
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
