package controllers

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func (c *ConversationsController) switchProvider(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, r.Method, r.URL.Path)
		return
	}
	var req ProviderSwitchRequest
	if !decodeConversationBody(w, r, &req) {
		return
	}
	// A message sent with the switch is checked before anything stops, so a
	// malformed one refuses the switch instead of following it.
	var message ports.ChatUserMessage
	if req.Message != nil {
		if len(req.Message.Attachments) > 0 {
			envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation", "INVALID_BODY",
				"send attachments separately after the switch", nil)
			return
		}
		var invalid *attachmentError
		if message, invalid = chatUserMessage(*req.Message); invalid != nil {
			envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation", invalid.code, invalid.message, nil)
			return
		}
	}
	sessionID := domain.SessionID(chi.URLParam(r, "sessionId"))
	// The switch and its message finish within the request's time limit even if
	// the client goes away (a restarted app); its retry of the message is a duplicate.
	ctx, cancel := withoutClientCancel(r.Context())
	defer cancel()
	err := c.Svc.SwitchProvider(ctx, sessionID, domain.ProviderSwitchRequest{
		TargetHarness: domain.AgentHarness(req.TargetHarness), IdempotencyKey: req.IdempotencyKey,
		Model:    capActivityMeta(strings.TrimSpace(domain.SanitizeControlChars(req.Model))),
		Effort:   capActivityMeta(strings.TrimSpace(domain.SanitizeControlChars(req.Effort))),
		FastMode: req.FastMode, Ultracode: req.Ultracode, AutoCompactWindow: req.AutoCompactWindow,
		PermissionMode: domain.PermissionMode(req.PermissionMode),
	})
	// A message retried after its switch went through just goes to that agent.
	if req.Message != nil && errors.Is(err, domain.ErrProviderAlreadyActive) {
		err = nil
	}
	var refusal *domain.ProviderSwitchError
	if errors.As(err, &refusal) {
		status, category := http.StatusConflict, "conflict"
		switch refusal.Kind {
		case domain.ProviderSwitchRefusalUnsupported:
			status, category = http.StatusBadRequest, "validation"
		case domain.ProviderSwitchRefusalUnavailable:
			status, category = http.StatusServiceUnavailable, "unavailable"
		}
		envelope.WriteAPIError(w, r, status, category, "PROVIDER_SWITCH_"+string(refusal.Kind), refusal.Error(), providerSwitchRefusalDetails(refusal))
	} else if err != nil {
		writeConversationError(w, r, err)
	} else if req.Message == nil {
		envelope.WriteJSON(w, http.StatusOK, ProviderSwitchResponse{Status: "ok"})
	} else {
		// A send that fails now is not a switch failure: the header tells the
		// client the switch went through.
		turn, sendErr := c.Svc.Send(ctx, sessionID, message)
		if sendErr != nil {
			w.Header().Set(providerSwitchedHeader, "1")
			// The renderer calls the daemon cross-origin, so it reads the header only if exposed.
			w.Header().Set("Access-Control-Expose-Headers", providerSwitchedHeader)
			writeConversationError(w, r, sendErr)
			return
		}
		response := sendConversationMessageResponse(turn)
		envelope.WriteJSON(w, http.StatusOK, ProviderSwitchResponse{Status: "ok", Message: &response})
	}
}

// providerSwitchRefusalDetails names, as a new task's refusal does, what an agent
// refused for the chat's permission mode lacks and the modes it runs in (Pi).
func providerSwitchRefusalDetails(refusal *domain.ProviderSwitchError) map[string]any {
	var capabilityErr *ports.ChatCapabilityError
	if !errors.As(refusal, &capabilityErr) {
		return nil
	}
	missing := make([]string, 0, len(capabilityErr.Missing))
	for _, capability := range capabilityErr.Missing {
		missing = append(missing, string(capability))
	}
	allowed := make([]string, 0, len(capabilityErr.AllowedPermissionModes))
	for _, mode := range capabilityErr.AllowedPermissionModes {
		allowed = append(allowed, string(mode))
	}
	return map[string]any{"missingCapabilities": missing, "allowedApprovalModes": allowed}
}

// providerSwitchedHeader marks an error response to a switch that carried a
// message: the switch completed, and only the message's send failed.
const providerSwitchedHeader = "X-AO-Provider-Switched"

// sessionHistory serves `ao session history`: a session's turns in full, newest
// first, for what a handoff note left out.
func (c *ConversationsController) sessionHistory(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, r.Method, r.URL.Path)
		return
	}
	query := r.URL.Query()
	offset, offsetErr := strconv.Atoi(cmp.Or(query.Get("offset"), "0"))
	limit, limitErr := strconv.Atoi(cmp.Or(query.Get("limit"), "10"))
	if offsetErr != nil || limitErr != nil || offset < 0 || limit < 1 {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation", "INVALID_HISTORY_PAGE", "offset must be 0 or more and limit 1 or more", nil)
		return
	}
	turns, total, err := c.Svc.SessionHistory(r.Context(), domain.SessionID(chi.URLParam(r, "sessionId")), offset, min(limit, 50))
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, SessionHistoryResponse{Turns: turns, Total: total, Offset: offset})
}

// withoutClientCancel keeps the request's deadline but not its cancellation
// when the client disconnects.
func withoutClientCancel(parent context.Context) (context.Context, context.CancelFunc) {
	detached := context.WithoutCancel(parent)
	if deadline, ok := parent.Deadline(); ok {
		return context.WithDeadline(detached, deadline)
	}
	return context.WithCancel(detached)
}
