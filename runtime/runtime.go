// Package runtime is the public entry point for embedding the Iris agent
// runtime. It wires a provider, tools, security policy, and execution budgets
// together without exposing internal implementation packages.
package runtime

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/m4ns0ur/iris/agent"
	internalagent "github.com/m4ns0ur/iris/internal/agent"
	"github.com/m4ns0ur/iris/internal/policy"
	"github.com/m4ns0ur/iris/provider"
	"github.com/m4ns0ur/iris/tool"
)

// Security configures the filesystem and shell execution boundary.
type Security struct {
	// Mode is "read-only", "workspace-write", or "danger-full-access".
	Mode string

	// ExternalSandbox acknowledges that Iris runs inside a container or VM.
	ExternalSandbox bool

	// ReadPaths and WritePaths grant access outside the workspace.
	ReadPaths  []string
	WritePaths []string

	// Shell configures shell command execution.
	Shell ShellSecurity
}

// ShellSecurity configures the shell execution policy.
type ShellSecurity struct {
	Mode               string
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
	Binary             string
}

// Options configures a Runtime.
type Options struct {
	// Workspace is the canonical agent workspace. Empty uses the process
	// working directory.
	Workspace string

	// Provider is the LLM backend. Required.
	Provider provider.Provider

	// Model is forwarded to the provider. Empty lets the provider choose its
	// default.
	Model string

	// Tools are additional tools registered after the built-in tools.
	Tools []tool.Tool

	// MaxTurns bounds LLM turns per run. Zero uses the runtime default.
	MaxTurns int

	// MaxToolCalls bounds tool executions per run. Zero uses the runtime
	// default.
	MaxToolCalls int

	// ToolTimeout bounds a single tool execution. Zero uses the runtime
	// default.
	ToolTimeout time.Duration

	// Security configures the execution boundary. Zero values use secure
	// defaults.
	Security Security

	// Approver is an optional initial approval mechanism. Consumers may also
	// install one later with Agent.SetApprover.
	Approver agent.Approver
}

// ErrRuntimeClosed is returned when creating an agent after Close.
var ErrRuntimeClosed = errors.New("runtime is closed")

// Runtime creates agents that share a provider, tool set, and security policy.
type Runtime interface {
	// NewAgent creates an agent with isolated conversation and tool state.
	NewAgent() (agent.Agent, error)

	// Warnings returns security warnings for the configured policy.
	Warnings() []string

	// Close closes all agents created by this runtime.
	Close() error
}

type runtimeImpl struct {
	workspace    string
	provider     provider.Provider
	model        string
	tools        []tool.Tool
	maxTurns     int
	maxToolCalls int
	toolTimeout  time.Duration
	approver     agent.Approver
	policyOpts   policy.Options
	warnings     []string

	mu     sync.Mutex
	closed bool
	agents []agent.Agent
}

// New validates the options and creates a runtime.
func New(options Options) (Runtime, error) {
	if options.Provider == nil {
		return nil, fmt.Errorf("runtime requires a provider")
	}

	workspace := options.Workspace
	if workspace == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("determine working directory: %w", err)
		}
		workspace = cwd
	}

	policyOpts := policyOptions(options.Security)

	// Validate the policy once so configuration errors surface at construction
	// rather than on the first run.
	probe, err := policy.New(workspace, policyOpts)
	if err != nil {
		return nil, err
	}

	return &runtimeImpl{
		workspace:    workspace,
		provider:     options.Provider,
		model:        options.Model,
		tools:        append([]tool.Tool(nil), options.Tools...),
		maxTurns:     options.MaxTurns,
		maxToolCalls: options.MaxToolCalls,
		toolTimeout:  options.ToolTimeout,
		approver:     options.Approver,
		policyOpts:   policyOpts,
		warnings:     probe.Warnings(),
	}, nil
}

func (r *runtimeImpl) NewAgent() (agent.Agent, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, ErrRuntimeClosed
	}
	r.mu.Unlock()

	pol, err := policy.New(r.workspace, r.policyOpts)
	if err != nil {
		return nil, err
	}

	instance, err := internalagent.NewAgent(r.provider, pol, internalagent.Options{
		Model:        r.model,
		MaxTurns:     r.maxTurns,
		MaxToolCalls: r.maxToolCalls,
		ToolTimeout:  r.toolTimeout,
		Tools:        r.tools,
	})
	if err != nil {
		return nil, err
	}

	if r.approver != nil {
		instance.SetApprover(r.approver)
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = instance.Close()
		return nil, ErrRuntimeClosed
	}
	r.agents = append(r.agents, instance)
	r.mu.Unlock()

	return instance, nil
}

func (r *runtimeImpl) Warnings() []string {
	return append([]string(nil), r.warnings...)
}

func (r *runtimeImpl) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	agents := append([]agent.Agent(nil), r.agents...)
	r.agents = nil
	r.mu.Unlock()

	var errs []error
	for _, instance := range agents {
		if err := instance.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func policyOptions(security Security) policy.Options {
	return policy.Options{
		Mode:            policy.Mode(security.Mode),
		ReadPaths:       append([]string(nil), security.ReadPaths...),
		WritePaths:      append([]string(nil), security.WritePaths...),
		ExternalSandbox: security.ExternalSandbox,
		Shell: policy.ShellOptions{
			Mode:               policy.ShellMode(security.Shell.Mode),
			Allow:              append([]string(nil), security.Shell.Allow...),
			Deny:               append([]string(nil), security.Shell.Deny...),
			DisableDefaultDeny: security.Shell.DisableDefaultDeny,
			ApprovalHook:       append([]string(nil), security.Shell.ApprovalHook...),
			Timeout:            security.Shell.Timeout,
			ApprovalTimeout:    security.Shell.ApprovalTimeout,
			MaxOutputBytes:     security.Shell.MaxOutputBytes,
			MaxOutputLines:     security.Shell.MaxOutputLines,
			MaxCaptureBytes:    security.Shell.MaxCaptureBytes,
			TempDirTTL:         security.Shell.TempDirTTL,
			ShellBinary:        security.Shell.Binary,
		},
	}
}
