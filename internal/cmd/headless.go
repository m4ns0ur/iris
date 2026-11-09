package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/m4ns0ur/iris/agent"
	"github.com/m4ns0ur/iris/client"
	"github.com/m4ns0ur/iris/rpc"
	"github.com/spf13/cobra"
)

var printCmd = &cobra.Command{
	Use:   "print [prompt]",
	Short: "Execute one prompt and print the final response",
	Long:  "Execute one prompt without the TUI and print only the final assistant response. Reads the prompt from stdin when no argument is given.",
	Args:  cobra.ArbitraryArgs,
	RunE:  runPrint,
}

var jsonlCmd = &cobra.Command{
	Use:   "jsonl [prompt]",
	Short: "Stream structured execution events as JSONL",
	Long:  "Execute one prompt without the TUI and write typed agent events as newline-delimited JSON. Reads the prompt from stdin when no argument is given.",
	Args:  cobra.ArbitraryArgs,
	RunE:  runJSONL,
}

var rpcCmd = &cobra.Command{
	Use:   "rpc",
	Short: "Control a headless agent session over JSON-RPC",
	Long:  "Serve JSON-RPC 2.0 over stdin/stdout. Agent events are emitted as agent.event notifications.",
	Args:  cobra.NoArgs,
	RunE:  runRPC,
}

func init() {
	rootCmd.AddCommand(printCmd, jsonlCmd, rpcCmd)
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func newHeadlessClient(env *environment) (*client.Client, error) {
	return client.New(client.Options{
		Agent:           env.agent,
		Sessions:        env.sessions,
		SessionDefaults: env.sessionDefaults,
	})
}

func promptFromArgs(args []string) (string, error) {
	if len(args) > 0 {
		prompt := strings.TrimSpace(strings.Join(args, " "))
		if prompt == "" {
			return "", fmt.Errorf("prompt is required")
		}
		return prompt, nil
	}

	info, err := os.Stdin.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect stdin: %w", err)
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return "", fmt.Errorf("prompt is required (pass an argument or pipe it on stdin)")
	}

	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("read prompt from stdin: %w", err)
	}
	prompt := strings.TrimSpace(string(data))
	if prompt == "" {
		return "", fmt.Errorf("prompt from stdin is empty")
	}
	return prompt, nil
}

func runPrint(_ *cobra.Command, args []string) error {
	prompt, err := promptFromArgs(args)
	if err != nil {
		return err
	}

	env, err := setupEnvironment()
	if err != nil {
		return err
	}
	defer env.Close()

	c, err := newHeadlessClient(env)
	if err != nil {
		return err
	}

	ctx, stop := signalContext()
	defer stop()

	result, runErr := c.Run(ctx, prompt)
	if result.Content != "" {
		fmt.Fprintln(os.Stdout, result.Content)
	}
	if runErr != nil {
		return runErr
	}
	return nil
}

type jsonlRecord struct {
	Type  string `json:"type"`
	Event any    `json:"event,omitempty"`
	Error string `json:"error,omitempty"`
}

func runJSONL(_ *cobra.Command, args []string) error {
	prompt, err := promptFromArgs(args)
	if err != nil {
		return err
	}

	env, err := setupEnvironment()
	if err != nil {
		return err
	}
	defer env.Close()

	c, err := newHeadlessClient(env)
	if err != nil {
		return err
	}

	ctx, stop := signalContext()
	defer stop()

	encoder := json.NewEncoder(os.Stdout)
	var writeErr error
	write := func(value any) {
		if writeErr != nil {
			return
		}
		if err := encoder.Encode(value); err != nil {
			writeErr = err
		}
	}

	result, runErr := c.RunStream(ctx, prompt, func(event agent.Event) {
		write(client.Record(event))
	})
	write(jsonlRecord{Type: "result", Event: result})

	if writeErr != nil {
		return fmt.Errorf("write JSONL output: %w", writeErr)
	}
	if runErr != nil {
		write(jsonlRecord{Type: "error", Error: runErr.Error()})
		if writeErr != nil {
			return fmt.Errorf("write JSONL output: %w", writeErr)
		}
		return runErr
	}
	return nil
}

func runRPC(_ *cobra.Command, _ []string) error {
	env, err := setupEnvironment()
	if err != nil {
		return err
	}
	defer env.Close()

	c, err := newHeadlessClient(env)
	if err != nil {
		return err
	}

	ctx, stop := signalContext()
	defer stop()

	// Decoding stdin is blocking. Closing it on cancellation lets Serve stop
	// cleanly on SIGINT/SIGTERM even while no request is being processed.
	go func() {
		<-ctx.Done()
		_ = os.Stdin.Close()
	}()

	server := rpc.NewServer(c)
	return server.Serve(ctx, os.Stdin, os.Stdout)
}
