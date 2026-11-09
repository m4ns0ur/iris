package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/m4ns0ur/iris/agent"
	"github.com/m4ns0ur/iris/internal/config"
	"github.com/m4ns0ur/iris/internal/contextmgr"
	"github.com/m4ns0ur/iris/internal/llm"
	"github.com/m4ns0ur/iris/internal/logger"
	"github.com/m4ns0ur/iris/internal/tui"
	"github.com/m4ns0ur/iris/resource"
	"github.com/m4ns0ur/iris/runtime"
	"github.com/m4ns0ur/iris/session"
)

type environment struct {
	cfg             config.Config
	workdir         string
	resources       *resource.Resources
	sessions        *session.JSONLManager
	rt              runtime.Runtime
	agent           agent.Agent
	sessionDefaults session.CreateOptions
}

func (e *environment) Close() {
	if e == nil {
		return
	}
	if e.agent != nil {
		if err := e.agent.Close(); err != nil {
			Logger.Warn("failed to clean up agent resources", "error", err)
		}
		e.agent = nil
	}
	if e.rt != nil {
		if err := e.rt.Close(); err != nil {
			Logger.Warn("failed to clean up runtime resources", "error", err)
		}
		e.rt = nil
	}
}

func setupEnvironment() (*environment, error) {
	if err := config.Load(); err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	logger.SetDebug(debug)

	if workdir != "" {
		info, err := os.Stat(workdir)
		if err != nil {
			return nil, fmt.Errorf("working directory %q: %w", workdir, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("working directory %q is not a directory", workdir)
		}
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("determine working directory: %w", err)
		}
		workdir = cwd
	}

	cfg := config.GetConfig()

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("determine home directory: %w", err)
	}

	sessionDir := cfg.Session.Dir
	if sessionDir == "" {
		sessionDir = filepath.Join(home, IrisHome, "sessions")
	}

	resources, err := resource.Load(resource.Options{
		GlobalDir: filepath.Join(home, IrisHome),
		Workdir:   workdir,
	})
	if err != nil {
		return nil, fmt.Errorf("load resources: %w", err)
	}
	for _, warning := range resources.Warnings {
		Logger.Warn("resource warning", "warning", warning)
		fmt.Fprintf(os.Stderr, "iris: resource warning: %s\n", warning)
	}

	sessions, err := session.NewJSONL(sessionDir)
	if err != nil {
		return nil, fmt.Errorf("initialize session store: %w", err)
	}

	modelSettings := config.DefaultModel()
	contextWindow := modelSettings.CtxSize
	if cfg.Context.Window > 0 {
		contextWindow = cfg.Context.Window
	}
	if contextWindow <= 0 {
		contextWindow = contextmgr.DefaultWindow
	}

	sessionDefaults := session.CreateOptions{
		Model:         cfg.DefaultModel,
		Workspace:     workdir,
		ModelMetadata: modelMetadata(cfg, contextWindow),
	}

	rt, err := runtime.New(runtime.Options{
		Workspace:        workdir,
		Provider:         llm.NewClient(),
		Model:            cfg.DefaultModel,
		MaxTurns:         cfg.Agent.MaxTurns,
		MaxToolCalls:     cfg.Agent.MaxToolCalls,
		ToolTimeout:      cfg.Agent.ToolTimeout(),
		ContextWindow:    contextWindow,
		ReserveTokens:    cfg.Context.ReserveTokens,
		CompactAt:        cfg.Context.CompactAt,
		KeepRecentTokens: cfg.Context.KeepRecentTokens,
		SummaryMaxTokens: cfg.Context.SummaryMaxTokens,
		Resources:        resources,
		Security:         runtimeSecurity(cfg.Security),
	})
	if err != nil {
		return nil, fmt.Errorf("invalid runtime configuration: %w", err)
	}

	env := &environment{
		cfg:             cfg,
		workdir:         workdir,
		resources:       resources,
		sessions:        sessions,
		rt:              rt,
		sessionDefaults: sessionDefaults,
	}

	for _, warning := range rt.Warnings() {
		Logger.Warn("security warning", "warning", warning)
		fmt.Fprintf(os.Stderr, "iris: warning: %s\n", warning)
	}

	instance, err := rt.NewAgent()
	if err != nil {
		env.Close()
		return nil, fmt.Errorf("create agent: %w", err)
	}
	env.agent = instance

	return env, nil
}

func runTUI(_ *cobra.Command, _ []string) error {
	env, err := setupEnvironment()
	if err != nil {
		return err
	}
	defer env.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return tui.Run(ctx, tui.Options{
		Agent:           env.agent,
		Sessions:        env.sessions,
		SessionDefaults: env.sessionDefaults,
		Resources:       env.resources,
	})
}
