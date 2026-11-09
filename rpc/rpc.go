// Package rpc exposes a JSON-RPC 2.0 server for controlling an Iris client
// over a bidirectional stream, typically stdin/stdout.
//
// Requests use standard JSON-RPC objects. Agent events are emitted as
// notifications:
//
//	{"jsonrpc":"2.0","method":"agent.event","params":{"type":"assistant.delta","event":{...}}}
//
// Methods:
//   - ping
//   - agent.run {"prompt":"..."}
//   - agent.cancel
//   - agent.steer {"prompt":"..."}
//   - agent.follow_up {"prompt":"..."}
//   - agent.clear_queue
//   - agent.status
//   - session.new
//   - session.resume {"id":"..."} (empty id resumes latest)
//   - session.list
//   - session.export
//   - session.fork {"entry_id":"..."} or {"user_index":1}
//   - session.current
//   - shutdown
package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/m4ns0ur/iris/agent"
	"github.com/m4ns0ur/iris/client"
	"github.com/m4ns0ur/iris/session"
)

// JSON-RPC error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
	codeBusy           = -32000
	codeIdle           = -32001
)

// Server serves JSON-RPC requests for one headless client.
type Server struct {
	client *client.Client

	writeMu  sync.Mutex
	encoder  *json.Encoder
	writeErr error

	runMu   sync.Mutex
	running bool
}

// NewServer creates an RPC server around a headless client.
func NewServer(c *client.Client) *Server {
	return &Server{client: c}
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type sessionInfo struct {
	ID                string    `json:"id"`
	Model             string    `json:"model,omitempty"`
	Workspace         string    `json:"workspace,omitempty"`
	ParentSessionID   string    `json:"parent_session_id,omitempty"`
	ForkedFromEntryID string    `json:"forked_from_entry_id,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	Entries           int       `json:"entries"`
}

// Serve reads JSON-RPC requests from in until EOF, a shutdown request, or a
// read error. Agent events are written to out as notifications.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.writeMu.Lock()
	s.encoder = json.NewEncoder(out)
	s.writeErr = nil
	s.writeMu.Unlock()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	decoder := json.NewDecoder(in)
	var runWG sync.WaitGroup
	shutdown := false

	for !shutdown {
		var req request
		if err := decoder.Decode(&req); err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				break
			}
			_ = s.writeProtocolError(nil, codeParse, "invalid JSON", nil)
			break
		}

		if req.JSONRPC != "2.0" || req.Method == "" {
			_ = s.writeProtocolError(req.ID, codeInvalidRequest, "invalid JSON-RPC request", nil)
			continue
		}

		switch req.Method {
		case "agent.run":
			var params struct {
				Prompt string `json:"prompt"`
			}
			if err := decodeParams(req.Params, &params); err != nil {
				_ = s.writeError(req.ID, codeInvalidParams, err.Error(), nil)
				continue
			}
			if strings.TrimSpace(params.Prompt) == "" {
				_ = s.writeError(req.ID, codeInvalidParams, "prompt is required", nil)
				continue
			}
			if !s.beginRun() {
				_ = s.writeError(req.ID, codeBusy, "agent is already running", nil)
				continue
			}

			id := cloneRaw(req.ID)
			prompt := params.Prompt
			started := make(chan struct{})
			done := make(chan struct{})
			var startOnce sync.Once

			runWG.Add(1)
			go func() {
				defer runWG.Done()
				defer s.endRun()
				defer close(done)

				result, err := s.client.RunStream(ctx, prompt, func(event agent.Event) {
					if _, ok := event.(agent.AgentStarted); ok {
						startOnce.Do(func() { close(started) })
					}
					_ = s.notify("agent.event", client.Record(event))
				})
				if err != nil {
					_ = s.writeError(id, codeInternal, err.Error(), result)
					return
				}
				_ = s.writeResult(id, result)
			}()

			// Do not process the next request until the run has started (or
			// failed to start). This makes a steer/follow-up sent immediately
			// after agent.run deterministic instead of racing Run.
			select {
			case <-started:
			case <-done:
			case <-ctx.Done():
			}

		case "agent.cancel":
			wasRunning := s.isRunning()
			queue := s.client.Abort()
			_ = s.writeResult(req.ID, map[string]any{
				"canceled":   wasRunning,
				"steering":   emptyIfNil(queue.Steering),
				"follow_ups": emptyIfNil(queue.FollowUps),
			})

		case "agent.steer", "agent.follow_up":
			var params struct {
				Prompt string `json:"prompt"`
			}
			if err := decodeParams(req.Params, &params); err != nil {
				_ = s.writeError(req.ID, codeInvalidParams, err.Error(), nil)
				continue
			}
			if strings.TrimSpace(params.Prompt) == "" {
				_ = s.writeError(req.ID, codeInvalidParams, "prompt is required", nil)
				continue
			}

			var err error
			if req.Method == "agent.steer" {
				err = s.client.Steer(params.Prompt)
			} else {
				err = s.client.FollowUp(params.Prompt)
			}
			if err != nil {
				code := codeInternal
				if errors.Is(err, agent.ErrAgentIdle) {
					code = codeIdle
				}
				_ = s.writeError(req.ID, code, err.Error(), nil)
				continue
			}
			_ = s.writeResult(req.ID, map[string]any{"queued": true})

		case "agent.clear_queue":
			queue := s.client.ClearQueue()
			_ = s.writeResult(req.ID, map[string]any{
				"steering":   emptyIfNil(queue.Steering),
				"follow_ups": emptyIfNil(queue.FollowUps),
			})

		case "agent.status":
			queue := s.client.Pending()
			_ = s.writeResult(req.ID, map[string]any{
				"running":            s.isRunning(),
				"session_id":         s.client.SessionID(),
				"pending_steering":   len(queue.Steering),
				"pending_follow_ups": len(queue.FollowUps),
			})

		case "session.new":
			if !s.ensureIdle(req.ID) {
				continue
			}
			created, err := s.client.NewSession()
			if err != nil {
				_ = s.writeError(req.ID, codeInternal, err.Error(), nil)
				continue
			}
			_ = s.writeResult(req.ID, infoForSession(created))

		case "session.resume":
			if !s.ensureIdle(req.ID) {
				continue
			}
			var params struct {
				ID string `json:"id"`
			}
			if err := decodeParams(req.Params, &params); err != nil && len(req.Params) > 0 {
				_ = s.writeError(req.ID, codeInvalidParams, err.Error(), nil)
				continue
			}
			var (
				loaded *session.Session
				err    error
			)
			if params.ID == "" {
				loaded, err = s.client.ResumeLatest()
			} else {
				loaded, err = s.client.ResumeSession(params.ID)
			}
			if err != nil {
				_ = s.writeError(req.ID, codeInternal, err.Error(), nil)
				continue
			}
			_ = s.writeResult(req.ID, infoForSession(loaded))

		case "session.list":
			summaries, err := s.client.ListSessions()
			if err != nil {
				_ = s.writeError(req.ID, codeInternal, err.Error(), nil)
				continue
			}
			_ = s.writeResult(req.ID, summaries)

		case "session.export":
			if !s.ensureIdle(req.ID) {
				continue
			}
			path, err := s.client.ExportSession()
			if err != nil {
				_ = s.writeError(req.ID, codeInternal, err.Error(), nil)
				continue
			}
			_ = s.writeResult(req.ID, map[string]any{"path": path})

		case "session.fork":
			if !s.ensureIdle(req.ID) {
				continue
			}
			var params struct {
				EntryID   string `json:"entry_id"`
				UserIndex int    `json:"user_index"`
			}
			if err := decodeParams(req.Params, &params); err != nil {
				_ = s.writeError(req.ID, codeInvalidParams, err.Error(), nil)
				continue
			}
			if params.EntryID == "" && params.UserIndex <= 0 {
				_ = s.writeError(req.ID, codeInvalidParams, "entry_id or user_index is required", nil)
				continue
			}

			var (
				child *session.Session
				err   error
			)
			if params.EntryID != "" {
				child, err = s.client.ForkSession(params.EntryID)
			} else {
				child, err = s.client.ForkSessionAt(params.UserIndex)
			}
			if err != nil {
				_ = s.writeError(req.ID, codeInternal, err.Error(), nil)
				continue
			}
			_ = s.writeResult(req.ID, infoForSession(child))

		case "session.current":
			_ = s.writeResult(req.ID, map[string]any{"id": s.client.SessionID()})

		case "ping":
			_ = s.writeResult(req.ID, "pong")

		case "shutdown":
			cancel()
			_ = s.writeResult(req.ID, map[string]any{"shutdown": true})
			shutdown = true

		default:
			_ = s.writeError(req.ID, codeMethodNotFound, "unknown method "+req.Method, nil)
		}
	}

	cancel()
	runWG.Wait()

	s.writeMu.Lock()
	err := s.writeErr
	s.writeMu.Unlock()
	return err
}

func (s *Server) beginRun() bool {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.running {
		return false
	}
	s.running = true
	return true
}

func (s *Server) endRun() {
	s.runMu.Lock()
	s.running = false
	s.runMu.Unlock()
}

func (s *Server) isRunning() bool {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	return s.running
}

func (s *Server) ensureIdle(id json.RawMessage) bool {
	if s.isRunning() {
		_ = s.writeError(id, codeBusy, "agent is running", nil)
		return false
	}
	return true
}

func (s *Server) writeResult(id json.RawMessage, result any) error {
	if len(id) == 0 {
		return nil
	}
	return s.write(response{JSONRPC: "2.0", ID: cloneRaw(id), Result: result})
}

func (s *Server) writeError(id json.RawMessage, code int, message string, data any) error {
	if len(id) == 0 {
		return nil
	}
	return s.write(response{
		JSONRPC: "2.0",
		ID:      cloneRaw(id),
		Error:   &rpcError{Code: code, Message: message, Data: data},
	})
}

func (s *Server) writeProtocolError(id json.RawMessage, code int, message string, data any) error {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return s.write(response{
		JSONRPC: "2.0",
		ID:      cloneRaw(id),
		Error:   &rpcError{Code: code, Message: message, Data: data},
	})
}

func (s *Server) notify(method string, params any) error {
	return s.write(notification{JSONRPC: "2.0", Method: method, Params: params})
}

func (s *Server) write(value any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	if s.encoder == nil {
		return fmt.Errorf("rpc server is not serving")
	}
	if err := s.encoder.Encode(value); err != nil {
		s.writeErr = err
		return err
	}
	return nil
}

func decodeParams(raw json.RawMessage, target any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	return nil
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}

func emptyIfNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func infoForSession(s *session.Session) sessionInfo {
	return sessionInfo{
		ID:                s.ID,
		Model:             s.Model,
		Workspace:         s.Workspace,
		ParentSessionID:   s.ParentSessionID,
		ForkedFromEntryID: s.ForkedFromEntryID,
		CreatedAt:         s.CreatedAt,
		UpdatedAt:         s.UpdatedAt,
		Entries:           len(s.Entries),
	}
}
