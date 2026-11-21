package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
	"github.com/complynx/loom/internal/modules"
	"github.com/complynx/loom/internal/operations"
)

const (
	externalWriteCatalog        = "write_catalog"
	externalWrite               = "write"
	externalWriteContinue       = "write_continue"
	externalWriteStatus         = "write_status"
	externalWritePageSize       = 12
	externalDeliveryUnconfirmed = "unconfirmed"
)

func externalWriteTools() []domain.ExternalTool {
	operation := json.RawMessage(`{"type":"object","properties":{"operation_id":{"type":"string"},` +
		`"token_service":{"type":"string"}},"required":["operation_id"],"additionalProperties":false}`)
	return []domain.ExternalTool{
		{
			Name:        externalWriteCatalog,
			Description: "List available write tools with input schemas. Use next_offset to page.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"offset":{"type":"integer","minimum":0},` +
				`"module":{"type":"string"},"token_service":{"type":"string"}},"additionalProperties":false}`),
		},
		{Name: externalWrite, Description: "Prepare a write using a UUID request_id. Reuse it on retry. " +
			"Keep the returned operation_id for continuation after Telegram approval or to retry delivery if the prompt did not arrive.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"module":{"type":"string"},` +
				`"method":{"type":"string"},"request_id":{"type":"string"},"arguments":{"type":"object"},` +
				`"token_service":{"type":"string"}},"required":["module","method","request_id","arguments"],` +
				`"additionalProperties":false}`)},
		{
			Name:        externalWriteStatus,
			Description: "Read the state of a write from this MCP connection. Never executes it.",
			InputSchema: operation,
		},
		{
			Name:        externalWriteContinue,
			Description: "Resume Telegram delivery if the prompt did not arrive, or execute the same write after approval.",
			InputSchema: operation,
		},
	}
}

type externalWriteInput struct {
	Arguments    json.RawMessage `json:"arguments,omitempty"`
	TokenService string          `json:"token_service,omitempty"`
	OperationID  string          `json:"operation_id,omitempty"`
	RequestID    string          `json:"request_id,omitempty"`
	Module       string          `json:"module,omitempty"`
	Method       string          `json:"method,omitempty"`
	Offset       int             `json:"offset,omitempty"`
}

func (a *ExternalAgent) callExternalWrite(
	ctx context.Context, caller domain.ExternalCaller, name string, raw json.RawMessage,
) (json.RawMessage, error) {
	switch name {
	case externalWriteCatalog, externalWrite, externalWriteContinue, externalWriteStatus:
	default:
		return nil, &domain.ToolError{Code: "unknown_tool", Recovery: "List tools available for this connection."}
	}
	if !a.limiter.allow(externalLimitKey(caller), time.Now()) {
		return nil, &domain.ToolError{Code: "rate_limited", Recovery: "Wait one minute before retrying."}
	}
	var input externalWriteInput
	if err := decodeDiagnosticsInput(raw, &input); err != nil || input.Offset < 0 {
		return nil, externalWriteArguments()
	}
	session, err := a.readmit(ctx, caller)
	if err != nil {
		return nil, err
	}
	if !session.profile.Writes {
		return nil, &domain.ToolError{
			Code:     "external_writes_disabled",
			Recovery: "Enable service writes in the confirming client.",
		}
	}
	session.tokenService, err = a.selectTokenService(input.TokenService)
	if err != nil {
		return nil, err
	}
	if name == externalWriteCatalog {
		return a.externalActionCatalog(ctx, session, input)
	}
	return a.externalOperation(ctx, session, name, input)
}

func externalWriteArguments() error {
	return &domain.ToolError{
		Code:     invalidArgumentsCode,
		Recovery: "Use the tool's input schema and keep the original operation ID.",
	}
}

func (a *ExternalAgent) externalActionCatalog(
	ctx context.Context, session externalSession, input externalWriteInput,
) (json.RawMessage, error) {
	p, err := a.servicePrincipal(ctx, session)
	if err != nil {
		return nil, err
	}
	tools, err := a.tools.registry.ListExternalActions(ctx, p, session.tokenService)
	if err != nil {
		return nil, err
	}
	filtered := make([]modules.Tool, 0, len(tools))
	for _, tool := range tools {
		if input.Module == "" || input.Module == tool.Module {
			filtered = append(filtered, tool)
		}
	}
	offset := min(input.Offset, len(filtered))
	page := min(offset+externalWritePageSize, len(filtered))
	var next *int
	if page < len(filtered) {
		next = &page
	}
	return json.Marshal(struct {
		Methods []modules.Tool `json:"methods"`
		Next    *int           `json:"next_offset,omitempty"`
	}{filtered[offset:page], next})
}

func (a *ExternalAgent) externalWriteBackend(session externalSession) modules.WriteBackend {
	return modules.WriteBackend{
		Registry:         a.tools.registry,
		ExternalWriteKey: a.tools.cfg.ExternalAgents.TokenServices[session.tokenService],
		Guard: func(ctx context.Context) error {
			fresh, err := a.admit(ctx, session.caller)
			if err != nil {
				return err
			}
			if !fresh.profile.Writes || fresh.profile.ConfirmedBy != session.profile.ConfirmedBy ||
				!tokenServiceListed(a.availableTokenServices(), session.tokenService) {
				return domain.Forbidden()
			}
			return a.CheckSession(ctx, session.caller)
		},
		RefreshPrincipal: func(ctx context.Context) (modules.Principal, error) {
			fresh, err := a.readmit(identity.RequireFresh(ctx), session.caller)
			if err != nil {
				return modules.Principal{}, err
			}
			if !fresh.profile.Writes || fresh.profile.ConfirmedBy != session.profile.ConfirmedBy {
				return modules.Principal{}, domain.Forbidden()
			}
			fresh.tokenService = session.tokenService
			return a.servicePrincipal(ctx, fresh)
		},
	}
}

func (a *ExternalAgent) externalOperation(
	ctx context.Context, session externalSession, name string, input externalWriteInput,
) (json.RawMessage, error) {
	if session.tokenService == "" {
		return nil, externalWriteArguments()
	}
	sessionID, owner, err := a.tools.repository.ExternalAgentSessionOwner(ctx, session.caller)
	if err != nil {
		return nil, externalSessionError(err)
	}
	coordinator := operations.Coordinator{
		Pool: a.tools.repository.Pool(), ArgumentCipher: a.tools.externalArgumentCipher,
		ApprovalTTL: time.Duration(a.tools.cfg.BootstrapSettings.ApprovalWaitSeconds) * time.Second,
	}
	backend := a.externalWriteBackend(session)
	var op operations.Operation
	var replay bool
	if name == externalWrite {
		op, replay, err = a.prepareExternalOperation(ctx, &coordinator, backend, session, sessionID, owner, input)
	} else {
		if _, parseErr := uuid.Parse(input.OperationID); parseErr != nil {
			return nil, externalWriteArguments()
		}
		op, err = coordinator.Get(ctx, input.OperationID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (op.ExternalSessionID != sessionID ||
			op.Binding.UserID != owner || op.ExternalService != session.tokenService ||
			op.ExternalConfirmer != session.profile.ConfirmedBy)) {
			return nil, &domain.ToolError{
				Code:     "operation_not_found",
				Recovery: "Use the operation ID from this connection.",
			}
		}
	}
	if err != nil {
		if errors.Is(err, operations.ErrExternalPendingLimit) {
			return nil, &domain.ToolError{
				Code:     "approval_limit",
				Recovery: "Resolve the pending Telegram approvals first.",
			}
		}
		return nil, err
	}
	if replay || name == externalWriteStatus || terminalExternalOperation(op) {
		return a.externalOperationStatus(ctx, &coordinator, backend, session, op)
	}
	return a.continueExternalOperation(ctx, &coordinator, backend, session, op)
}

func terminalExternalOperation(op operations.Operation) bool {
	return op.State == externalOutcomeDone || op.State == externalOutcomeFailed || op.State == operationRejected
}

func (a *ExternalAgent) externalOperationStatus(
	ctx context.Context, coordinator *operations.Coordinator, backend modules.WriteBackend,
	session externalSession, op operations.Operation,
) (json.RawMessage, error) {
	switch op.State {
	case outcomeUnknown, operationPending, operationDispatching, operationPreparing:
		var err error
		op, err = coordinator.ReconcileExternal(ctx, op, backend)
		if err != nil {
			return nil, err
		}
	}
	result, err := backend.ReadExternalReceipt(ctx, op)
	if err != nil {
		return nil, err
	}
	op.Result = result
	return a.recordExternalWriteReceipt(ctx, session, op)
}

func (a *ExternalAgent) prepareExternalOperation(
	ctx context.Context, coordinator *operations.Coordinator, backend modules.WriteBackend,
	session externalSession, sessionID, owner string, input externalWriteInput,
) (operations.Operation, bool, error) {
	requestID, err := uuid.Parse(input.RequestID)
	if err != nil || requestID == uuid.Nil || len(input.Arguments) == 0 {
		return operations.Operation{}, false, externalWriteArguments()
	}
	caller := session.caller.Principal
	request := operations.Request{
		ExternalSessionID: sessionID, ExternalService: session.tokenService, ExternalClient: session.caller.ClientID,
		ExternalConfirmer: session.profile.ConfirmedBy,
		CallID:            requestID.String(), Module: input.Module, Method: input.Method, Arguments: input.Arguments,
		Binding: operations.Binding{UserID: owner, Issuer: caller.Issuer, Subject: caller.Subject,
			Actor: caller.Actor, OrgID: caller.OrgID},
	}
	prior, err := coordinator.FindExternalPreparation(ctx, request)
	if !errors.Is(err, pgx.ErrNoRows) {
		return prior, true, err
	}
	p, err := backend.RefreshPrincipal(ctx)
	if err != nil {
		return operations.Operation{}, false, err
	}
	descriptor, err := a.tools.registry.DescribeExternalAction(ctx, p, session.tokenService, input.Module, input.Method)
	if err != nil {
		return operations.Operation{}, false, err
	}
	request.Binding.ContractHash = descriptor.ContractHash
	request.RememberApproval = descriptor.Method.ExternalAgentRemember
	request.Mode = descriptor.Method.Confirmation.Mode
	request.ProtectArguments = descriptor.Method.ExternalAgentProtectedArguments
	return coordinator.PrepareExternal(ctx, request, backend)
}

func (a *ExternalAgent) continueExternalOperation(
	ctx context.Context, coordinator *operations.Coordinator, backend modules.WriteBackend,
	session externalSession, op operations.Operation,
) (json.RawMessage, error) {
	after, err := coordinator.DispatchExternal(ctx, op, backend.Authorize, backend)
	switch {
	case errors.Is(err, operations.ErrApprovalRequired):
		notice := domain.ExternalApproval{
			ID:          op.ID,
			Subject:     op.Binding.Subject,
			OrgID:       op.Binding.OrgID,
			Client:      externalClientID(op.ExternalClient),
			Service:     op.ExternalService,
			Module:      op.Module,
			Method:      op.Method,
			Description: op.Preparation.Description,
			Arguments:   op.Preparation.Arguments,
			Remember:    op.RememberApproval,
			ExpiresAt:   op.Preparation.ExpiresAt,
		}
		delivery := "telegram"
		if err = a.tools.registry.DeliverExternalApproval(ctx, op.Binding.Issuer, op.ExternalConfirmer,
			a.tools.cfg.ExternalAgents.TokenServices, notice); err != nil {
			delivery = externalDeliveryUnconfirmed
		}
		return externalWriteReceipt(after, delivery)
	case errors.Is(err, operations.ErrExpired):
		return externalWriteReceipt(after, "")
	case errors.Is(err, operations.ErrState):
		return a.externalOperationStatus(ctx, coordinator, backend, session, after)
	case err != nil && after.State != outcomeUnknown && after.State != externalOutcomeFailed:
		return nil, err
	}
	return a.recordExternalWriteReceipt(ctx, session, after)
}

func (a *ExternalAgent) recordExternalWriteReceipt(
	ctx context.Context, session externalSession, op operations.Operation,
) (json.RawMessage, error) {
	switch op.State {
	case externalOutcomeDone, externalOutcomeFailed, outcomeUnknown, operationPending:
		p := session.caller.Principal
		eventID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(op.ID+":"+op.State)).String()
		event := domain.ExternalAction{
			ID: eventID, ClientID: externalClientID(op.ExternalClient), Tool: externalWrite, Outcome: op.State,
			Calls: []domain.ExternalActionCall{{Module: op.Module, Method: op.Method, Count: 1}},
		}
		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), externalRecordTimeout)
		defer cancel()
		if _, logErr := a.tools.repository.AppendExternalAction(
			recordCtx,
			p,
			event,
			fmt.Sprintf(
				"External agent %s requested %s.%s. Outcome: %s.",
				event.ClientID,
				op.Module,
				op.Method,
				op.State,
			),
		); logErr != nil {
			return nil, domain.NewError(http.StatusServiceUnavailable, "history_unavailable")
		}
	}
	if err := a.CheckSession(ctx, session.caller); err != nil {
		return nil, externalSessionError(err)
	}
	return externalWriteReceipt(op, "")
}

func externalWriteReceipt(op operations.Operation, delivery string) (json.RawMessage, error) {
	status := op.State
	if status == "prepared" || status == "approved" {
		switch {
		case !op.Preparation.ExpiresAt.After(time.Now()):
			status = "expired"
		case status == "prepared":
			status = "waiting_approval"
			if delivery == "" {
				delivery = externalDeliveryUnconfirmed
			}
		}
	}
	recovery := ""
	if status == "waiting_approval" && delivery == externalDeliveryUnconfirmed {
		recovery = "If the Telegram prompt did not arrive, call write_continue with this operation_id to resume delivery."
	}
	return json.Marshal(struct {
		ID       string          `json:"operation_id"`
		Status   string          `json:"status"`
		Delivery string          `json:"approval_delivery,omitempty"`
		Result   json.RawMessage `json:"result,omitempty"`
		Failure  string          `json:"failure_code,omitempty"`
		Recovery string          `json:"recovery,omitempty"`
	}{op.ID, status, delivery, op.Result, op.FailureCode, recovery})
}
