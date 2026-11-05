package policy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/m4ns0ur/iris/internal/proc"
)

// ApprovalRequest describes an action that needs a human or hook decision.
type ApprovalRequest struct {
	// Kind identifies the action type. It is currently always "shell".
	Kind string `json:"kind"`

	// Command is the raw command that would be executed.
	Command string `json:"command"`

	// Workdir is the directory the command would run in.
	Workdir string `json:"workdir"`

	// Programs lists the command names discovered by the static analyser.
	Programs []string `json:"programs,omitempty"`

	// Reason explains why approval is required (for example, the command is
	// not allowlisted or uses an opaque shell construct).
	Reason string `json:"reason,omitempty"`
}

// ApprovalDecision is the answer to an ApprovalRequest.
type ApprovalDecision struct {
	// Allow reports whether the action may proceed.
	Allow bool `json:"allow"`

	// Reason is an optional human-readable explanation. It is shown to the
	// user/agent when access is denied.
	Reason string `json:"reason,omitempty"`
}

// Approver decides whether a risky action may run. Implementations must
// respect the supplied context and return promptly when it is cancelled.
type Approver interface {
	Approve(ctx context.Context, req ApprovalRequest) (ApprovalDecision, error)
}

// ApproverFunc adapts a function to the Approver interface. It is useful for
// embedding custom approval UI or policy extensions.
type ApproverFunc func(ctx context.Context, req ApprovalRequest) (ApprovalDecision, error)

// Approve implements Approver.
func (f ApproverFunc) Approve(ctx context.Context, req ApprovalRequest) (ApprovalDecision, error) {
	return f(ctx, req)
}

// HookApprover runs an external program to decide approval requests. The
// request is sent as JSON on stdin. Exit status 0 allows the command, any
// non-zero status denies it; stdout/stderr are used as the denial reason.
//
// The hook is executed directly (never through a shell) and is bounded by a
// timeout and an output limit so that a broken hook cannot hang Iris.
type HookApprover struct {
	Command   []string
	Timeout   time.Duration
	MaxOutput int
}

// Approve implements Approver.
func (h *HookApprover) Approve(ctx context.Context, req ApprovalRequest) (ApprovalDecision, error) {
	if h == nil || len(h.Command) == 0 || strings.TrimSpace(h.Command[0]) == "" {
		return ApprovalDecision{}, ErrApprovalUnavailable
	}

	timeout := h.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	payload, err := json.Marshal(req)
	if err != nil {
		return ApprovalDecision{}, fmt.Errorf("encode approval request: %w", err)
	}

	cmd := exec.CommandContext(ctx, h.Command[0], h.Command[1:]...)
	proc.Configure(cmd)
	cmd.Cancel = func() error { return proc.Kill(cmd) }
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Env = append(os.Environ(), "IRIS_APPROVAL_KIND="+req.Kind, "IRIS_WORKDIR="+req.Workdir)

	maxOutput := h.MaxOutput
	if maxOutput <= 0 {
		maxOutput = 64 * 1024
	}
	out := &limitedBuffer{max: maxOutput}
	cmd.Stdout = out
	cmd.Stderr = out

	runErr := cmd.Run()
	reason := strings.TrimSpace(out.String())

	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return ApprovalDecision{}, fmt.Errorf("%w after %s", ErrApprovalTimeout, timeout)
		}
		return ApprovalDecision{}, ctxErr
	}

	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			if reason == "" {
				reason = fmt.Sprintf("approval hook exited with status %d", exitErr.ExitCode())
			}
			return ApprovalDecision{Allow: false, Reason: reason}, nil
		}
		return ApprovalDecision{}, fmt.Errorf("run approval hook: %w", runErr)
	}

	return ApprovalDecision{Allow: true, Reason: reason}, nil
}

// limitedBuffer keeps at most max bytes. Additional writes are accepted and
// discarded so that a noisy process cannot exhaust memory.
type limitedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.max <= 0 {
		return n, nil
	}
	remaining := b.max - b.buf.Len()
	if remaining > 0 {
		if remaining > n {
			remaining = n
		}
		_, _ = b.buf.Write(p[:remaining])
	}
	return n, nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
