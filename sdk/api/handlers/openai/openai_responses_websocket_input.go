package openai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// responsesLocalInterrupt cancels an in-flight HTTP turn. A websocket upstream
// does not use it; that interrupt is written to the existing socket instead.
const responsesInterruptTerminalLimit = 128

type responsesInterruptFrame struct {
	payload    []byte
	generation uint64
}

type responsesLocalInterrupt struct {
	mu             sync.Mutex
	active         bool
	http           bool
	generation     uint64
	responseID     string
	authID         string
	authCheck      func(string) bool
	authGeneration uint64
	terminalIDs    map[[sha256.Size]byte]struct{}
	terminalOrder  [][sha256.Size]byte
	frames         chan responsesInterruptFrame
}

func newResponsesLocalInterrupt() *responsesLocalInterrupt {
	return &responsesLocalInterrupt{frames: make(chan responsesInterruptFrame, 1), terminalIDs: make(map[[sha256.Size]byte]struct{})}
}

func (s *responsesLocalInterrupt) bindAuth(authID string, check func(string) bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.authGeneration++
	s.authID, s.authCheck = authID, check
	s.mu.Unlock()
}

func (s *responsesLocalInterrupt) authEnabled(authID string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	boundID, check, generation := s.authID, s.authCheck, s.authGeneration
	s.mu.Unlock()
	if authID == "" || authID != boundID || check == nil || !check(authID) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return generation == s.authGeneration && boundID == s.authID
}

func (s *responsesLocalInterrupt) begin(http bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.generation++
	s.active, s.http, s.responseID = true, http, ""
	s.mu.Unlock()
}

func (s *responsesLocalInterrupt) end() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.rememberTerminalLocked(s.responseID)
	s.active, s.http, s.responseID = false, false, ""
	select {
	case <-s.frames:
	default:
	}
	s.mu.Unlock()
}

func (s *responsesLocalInterrupt) rememberTerminalLocked(id string) {
	if id == "" {
		return
	}
	// Fixed-size digests bound retained history even for very large upstream IDs.
	digest := sha256.Sum256([]byte(id))
	if _, exists := s.terminalIDs[digest]; exists {
		return
	}
	if len(s.terminalOrder) >= responsesInterruptTerminalLimit {
		delete(s.terminalIDs, s.terminalOrder[0])
		copy(s.terminalOrder, s.terminalOrder[1:])
		s.terminalOrder = s.terminalOrder[:len(s.terminalOrder)-1]
	}
	s.terminalIDs[digest] = struct{}{}
	s.terminalOrder = append(s.terminalOrder, digest)
}

func (s *responsesLocalInterrupt) observe(payload []byte) {
	if s == nil {
		return
	}
	id := strings.TrimSpace(gjson.GetBytes(payload, "response.id").String())
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch gjson.GetBytes(payload, "type").String() {
	case "response.created":
		s.generation++
		s.responseID = id
	case "response.completed", "response.incomplete", "response.done", "response.failed":
		s.rememberTerminalLocked(id)
		if s.responseID == id {
			s.responseID = ""
		}
	}
}

func (s *responsesLocalInterrupt) deliver(payload []byte) bool {
	if s == nil {
		return false
	}
	id := strings.TrimSpace(gjson.GetBytes(payload, "response_id").String())
	s.mu.Lock()
	if !s.active || !s.http || id == "" || id != s.responseID {
		_, terminal := s.terminalIDs[sha256.Sum256([]byte(id))]
		s.mu.Unlock()
		return terminal
	}
	generation, authID := s.generation, s.authID
	s.mu.Unlock()
	if !s.authEnabled(authID) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active || !s.http || id != s.responseID || generation != s.generation {
		return false
	}
	select {
	case s.frames <- responsesInterruptFrame{payload: bytes.Clone(payload), generation: generation}:
	default:
	}
	return true
}

// take rechecks the turn because completion may race an already queued interrupt.
func (s *responsesLocalInterrupt) take(frame responsesInterruptFrame) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	authID := s.authID
	s.mu.Unlock()
	if !s.authEnabled(authID) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := strings.TrimSpace(gjson.GetBytes(frame.payload, "response_id").String())
	if !s.active || !s.http || frame.generation != s.generation || id == "" || id != s.responseID {
		return false
	}
	s.rememberTerminalLocked(id)
	s.responseID = ""
	return true
}

func (s *responsesLocalInterrupt) framesChan() <-chan responsesInterruptFrame {
	if s == nil {
		return nil
	}
	return s.frames
}

// readResponsesWebsocketInput is the only downstream reader. Interrupts are
// handled immediately so they are not queued behind an active response.
// The bounded queue rejects overload so queued requests cannot hide a disconnect.
func readResponsesWebsocketInput(ctx context.Context, cancel context.CancelCauseFunc, conn *websocket.Conn, interrupt func([]byte) error, local *responsesLocalInterrupt, writer *responsesWebsocketWriter, timeline websocketTimelineAppender) (<-chan cliproxyexecutor.WebsocketInput, <-chan struct{}) {
	input := make(chan cliproxyexecutor.WebsocketInput, wsPendingRequestLimit)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(input)
		for {
			kind, payload, errRead := conn.ReadMessage()
			if errRead != nil {
				cancel(errRead)
				return
			}
			if kind != websocket.TextMessage && kind != websocket.BinaryMessage {
				continue
			}
			// Control frames bypass response.create normalization. The original
			// response_id, mode, and extension fields must reach upstream unchanged.
			if json.Valid(payload) && gjson.GetBytes(payload, "type").String() == "response.interrupt" {
				appendResponsesInterruptDiagnostic(timeline, payload, "")
				errInterrupt := interrupt(payload)
				switch {
				case errInterrupt == nil:
					appendResponsesInterruptDiagnostic(timeline, payload, "handled")
					continue
				case errors.Is(errInterrupt, cliproxyexecutor.ErrNoActiveUpstreamWebsocket) && local.deliver(payload):
					appendResponsesInterruptDiagnostic(timeline, payload, "local_http")
					continue
				default:
					appendResponsesInterruptDiagnostic(timeline, payload, "rejected")
					// The sanitized outcome replaces raw error logging here: errors
					// from transport dependencies may contain credentials or URLs.
					_, errWrite := writeResponsesWebsocketError(writer, nil, &interfaces.ErrorMessage{
						StatusCode: http.StatusBadRequest,
						Error:      errInterrupt,
					})
					if errWrite != nil {
						cancel(errWrite)
						return
					}
					continue
				}
			}
			select {
			case input <- cliproxyexecutor.WebsocketInput{Payload: payload}:
			case <-ctx.Done():
				return
			default:
				cancel(errors.New("responses websocket: too many queued requests"))
				_, _ = writer.closeWithoutError()
				return
			}
		}
	}()
	return input, done
}

func appendResponsesInterruptDiagnostic(timeline websocketTimelineAppender, payload []byte, outcome string) {
	if timeline == nil {
		return
	}
	// Hash client-controlled IDs and omit all arbitrary fields, including mode.
	// Only correlation metadata is needed to diagnose control-frame timing.
	idHash := sha256.Sum256([]byte(gjson.GetBytes(payload, "response_id").String()))
	diagnostic := struct {
		Type           string `json:"type"`
		ResponseIDHash string `json:"response_id_hash"`
		Outcome        string `json:"outcome,omitempty"`
	}{Type: "response.interrupt", ResponseIDHash: fmt.Sprintf("%x", idHash[:8]), Outcome: outcome}
	data, errMarshal := json.Marshal(diagnostic)
	if errMarshal != nil {
		return
	}
	event := "interrupt"
	if outcome != "" {
		event = "interrupt.outcome"
	}
	timeline.Append(event, data, time.Now())
}

func (h *OpenAIResponsesAPIHandler) forwardResponsesWebsocketInterrupt(ctx context.Context, sessionID string, payload []byte, checks ...func(string) bool) error {
	responseID := gjson.GetBytes(payload, "response_id")
	if responseID.Type != gjson.String || strings.TrimSpace(responseID.String()) == "" {
		return fmt.Errorf("response.interrupt requires response_id")
	}
	if h == nil || h.AuthManager == nil {
		return cliproxyexecutor.ErrNoActiveUpstreamWebsocket
	}
	exec, ok := h.AuthManager.Executor("codex")
	if !ok || exec == nil {
		return cliproxyexecutor.ErrNoActiveUpstreamWebsocket
	}
	sender, ok := exec.(interface {
		InterruptExecutionSession(context.Context, string, []byte) error
	})
	if !ok || sender == nil {
		return fmt.Errorf("upstream executor does not support response.interrupt")
	}
	ctx = cliproxyexecutor.WithWebsocketAuthCheck(ctx, func(authID string) bool {
		// Home runtime credentials live on the execution session, not in the global map.
		auth, found := h.AuthManager.GetByID(authID)
		if !found || auth == nil {
			auth, found = h.AuthManager.GetExecutionSessionAuthByID(sessionID, authID)
		}
		if !found || auth == nil || auth.Disabled || auth.Status == coreauth.StatusDisabled {
			return false
		}
		return len(checks) == 0 || checks[0] == nil || checks[0](authID)
	})
	return sender.InterruptExecutionSession(ctx, sessionID, payload)
}
