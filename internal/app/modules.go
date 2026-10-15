package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
	"github.com/complynx/loom/internal/modules"
	"github.com/complynx/loom/internal/operations"
	"github.com/complynx/loom/internal/provider"
	"github.com/complynx/loom/internal/runs"
	"github.com/complynx/loom/internal/store"
	"github.com/complynx/loom/internal/telemetry"
)

type RunAuthorizer func(context.Context, domain.Run) (domain.Principal, string, error)

// ModuleBridge is the production binding between fenced runs and durable writes.
// It exposes no credentials or direct mutation path to JavaScript.
type ModuleBridge struct {
	observer    *telemetry.Telemetry
	registry    *modules.Registry
	coordinator *operations.Coordinator
	repository  *store.Store
	authorize   RunAuthorizer
}

func NewModuleBridge(
	registry *modules.Registry,
	coordinator *operations.Coordinator,
	repository *store.Store,
	authorize RunAuthorizer,
) *ModuleBridge {
	return &ModuleBridge{registry: registry, coordinator: coordinator, repository: repository, authorize: authorize}
}
func modulePrincipal(p domain.Principal, token string, revision int64) modules.Principal {
	permissions := []string{}
	for permission, enabled := range p.Permissions {
		if enabled {
			permissions = append(permissions, permission)
		}
	}
	return modules.Principal{
		Issuer:         p.Issuer,
		Subject:        p.Subject,
		Actor:          p.Actor,
		Organization:   p.OrgID,
		Permissions:    permissions,
		Token:          token,
		PolicyRevision: strconv.FormatInt(revision, 10),
	}
}
func (b *ModuleBridge) guard(ctx context.Context, run domain.Run) error {
	current, err := b.repository.ExecutionRun(ctx, run.ID)
	if err != nil {
		return err
	}
	if current.State != domain.Running || current.LeaseOwner != run.LeaseOwner || current.Fence != run.Fence ||
		current.PauseRequested ||
		current.CancelRequested {
		return operations.ErrLease
	}
	if current.LeaseUntil == nil || !current.LeaseUntil.After(time.Now()) {
		return operations.ErrLease
	}
	_, _, err = b.authorize(ctx, current)
	return err
}

func (b *ModuleBridge) Call(
	ctx context.Context,
	run domain.Run,
	p domain.Principal,
	_ string,
	call provider.Call,
) (json.RawMessage, *domain.Approval, error) {
	ctx = identity.RequireFresh(ctx)
	var args struct {
		Module    string          `json:"module"`
		Method    string          `json:"method"`
		Arguments json.RawMessage `json:"arguments"`
	}
	decoder := json.NewDecoder(bytes.NewReader(call.Arguments))
	decoder.DisallowUnknownFields()
	validArguments := decoder.Decode(&args) == nil
	if !validArguments || args.Module == "" || args.Method == "" || !json.Valid(args.Arguments) {
		return json.RawMessage(`{"error":"invalid_module_call"}`), nil, nil
	}
	fresh, credential, err := b.authorize(ctx, run)
	if err != nil {
		return nil, nil, err
	}
	mp := modulePrincipal(fresh, credential, run.SettingsSnapshot.Version)
	mp.UserID = run.OwnerID
	descriptor, err := b.registry.Describe(ctx, mp, args.Module, args.Method)
	if err != nil {
		return nil, nil, err
	}
	if descriptor.Method.Effect == builtinReadMethod {
		return json.RawMessage(`{"error":"use_script_for_reads"}`), nil, nil
	}
	backend := modules.WriteBackend{
		Registry:  b.registry,
		Principal: mp,
		RefreshPrincipal: func(c context.Context) (modules.Principal, error) {
			currentPrincipal, currentCredential, refreshErr := b.authorize(identity.RequireFresh(c), run)
			value := modulePrincipal(currentPrincipal, currentCredential, run.SettingsSnapshot.Version)
			value.UserID = run.OwnerID
			return value, refreshErr
		},
		Guard: func(c context.Context) error { return b.guard(c, run) },
	}
	request := operations.Request{
		RunID:     run.ID,
		CallID:    call.ID,
		Module:    args.Module,
		Method:    args.Method,
		Arguments: args.Arguments,
		Mode:      descriptor.Method.Confirmation.Mode,
		Binding: operations.Binding{
			UserID:       run.OwnerID,
			Issuer:       p.Issuer,
			Subject:      p.Subject,
			Actor:        p.Actor,
			OrgID:        p.OrgID,
			ContractHash: descriptor.ContractHash,
		},
	}
	operation, err := b.coordinator.Prepare(ctx, request, backend)

	if err != nil {
		b.logOperation(ctx, run.ID, operation)
		return nil, nil, err
	}
	return b.resolveOperation(ctx, run, args.Module, args.Method, operation, backend)
}

// ApproveTx commits approval and run wake-up together; it does not dispatch RPCs.
func (b *ModuleBridge) ApproveTx(ctx context.Context, tx pgx.Tx, run domain.Run, input domain.RunInput) error {
	if run.Approval == nil || input.ApprovalID != run.Approval.ID {
		return operations.ErrBinding
	}
	operation, err := b.coordinator.Get(ctx, run.Approval.OperationID)
	if err != nil {
		return err
	}
	if operation.RunID != run.ID || operation.Binding.UserID != run.OwnerID || operation.Binding.Actor != run.ActorID ||
		operation.Binding.OrgID != run.OrgID ||
		operation.BindingHash != run.Approval.ArgumentHash {
		return operations.ErrBinding
	}
	if input.Decision != "approve" && input.Decision != "reject" {
		return errors.New("invalid approval decision")
	}
	_, err = b.coordinator.ApproveTx(
		ctx,
		tx,
		operation.ID,
		operations.Approval{
			Binding:     operation.Binding,
			BindingHash: run.Approval.ArgumentHash,
			Approve:     input.Decision == "approve",
		},
	)
	return err
}

// Discard cancels only actions proven not to have dispatched. Ambiguous writes
// are resolved with status lookup; steering never replays or forgets an effect.
func (b *ModuleBridge) Discard(
	ctx context.Context,
	run domain.Run,
	_ domain.Principal,
	_ string,
	call provider.Call,
) (json.RawMessage, *domain.Approval, error) {
	op, found, err := b.coordinator.FindByCall(ctx, run.ID, call.ID)
	if err != nil {
		return nil, nil, err
	}
	cancelled := json.RawMessage(`{"status":"cancelled","reason":"steering"}`)
	if !found {
		return cancelled, nil, nil
	}
	fresh, credential, err := b.authorize(ctx, run)
	if err != nil {
		return nil, nil, err
	}
	if op.Binding.UserID != run.OwnerID || op.Binding.Issuer != fresh.Issuer || op.Binding.Subject != fresh.Subject ||
		op.Binding.Actor != fresh.Actor ||
		op.Binding.OrgID != fresh.OrgID {
		return nil, nil, operations.ErrBinding
	}
	op, proven, err := b.coordinator.CancelUndispatched(
		ctx,
		op.ID,
		operations.Lease{Owner: run.LeaseOwner, Fence: run.Fence},
		op.Binding,
	)
	if err != nil {
		return nil, nil, err
	}
	if proven {
		return cancelled, nil, nil
	}
	mp := modulePrincipal(fresh, credential, run.SettingsSnapshot.Version)
	mp.UserID = run.OwnerID
	backend := modules.WriteBackend{
		Registry:  b.registry,
		Principal: mp,
		RefreshPrincipal: func(c context.Context) (modules.Principal, error) {
			user, secret, refreshErr := b.authorize(identity.RequireFresh(c), run)
			value := modulePrincipal(user, secret, run.SettingsSnapshot.Version)
			value.UserID = run.OwnerID
			return value, refreshErr
		},
		Guard: func(c context.Context) error { return b.guard(c, run) },
	}
	if op.State == "dispatching" || op.State == "pending" || op.State == outcomeUnknown {
		op, err = b.coordinator.Reconcile(ctx, op.ID, backend)
		if err != nil {
			return nil, nil, &runs.Suspension{State: domain.Reconciling, Reason: operationUnknown}
		}
	}
	switch op.State {
	case "succeeded":
		if json.Valid(op.Result) {
			return op.Result, nil, nil
		}
		return json.RawMessage(`{"status":"succeeded"}`), nil, nil
	case "failed", "rejected":
		raw, _ := json.Marshal(map[string]string{"status": op.State})
		return raw, nil, nil
	default:
		return nil, nil, &runs.Suspension{State: domain.Reconciling, Reason: operationUnknown}
	}
}

func (b *ModuleBridge) resolveOperation(
	ctx context.Context,
	run domain.Run,
	module, method string,
	operation operations.Operation,
	backend modules.WriteBackend,
) (json.RawMessage, *domain.Approval, error) {
	defer func() { b.logOperation(ctx, run.ID, operation) }()
	var err error
	if operation.State == "prepared" {
		text := operation.Preparation.Description
		if text == "" {
			text = fmt.Sprintf("Confirm %s.%s", module, method)
		}
		approval := &domain.Approval{
			ID:           operation.ID,
			OperationID:  operation.ID,
			Method:       module + "." + method,
			Arguments:    operation.Preparation.Arguments,
			ArgumentHash: operation.BindingHash,
			Text:         text,
			ExpiresAt:    operation.Preparation.ExpiresAt,
		}
		return nil, approval, nil
	}
	if operation.State == "approved" {
		operation, err = b.coordinator.Dispatch(
			ctx,
			operation.ID,
			operations.Lease{Owner: run.LeaseOwner, Fence: run.Fence},
			backend.Authorize,
			backend,
		)
		if err != nil {
			if operation.State == outcomeUnknown {
				return nil, nil, &runs.Suspension{State: domain.Reconciling, Reason: operationUnknown}
			}
			return nil, nil, err
		}
	}
	if operation.State == outcomeUnknown || operation.State == "pending" || operation.State == "dispatching" {
		operation, err = b.coordinator.Reconcile(ctx, operation.ID, backend)
		if err != nil {
			return nil, nil, &runs.Suspension{State: domain.Reconciling, Reason: operationUnknown}
		}
	}
	switch operation.State {
	case "succeeded":
		if !json.Valid(operation.Result) {
			return json.RawMessage(`{"status":"succeeded"}`), nil, nil
		}
		return operation.Result, nil, nil
	case "rejected", "failed":
		raw, _ := json.Marshal(map[string]string{"status": operation.State})
		return raw, nil, nil
	default:
		return nil, nil, &runs.Suspension{State: domain.Reconciling, Reason: operationUnknown}
	}
}

// logOperation records the latest operation state observed by the bridge.
func (b *ModuleBridge) logOperation(ctx context.Context, runID string, operation operations.Operation) {
	if b.observer != nil && operation.ID != "" {
		b.observer.Event(
			ctx,
			"write operation checkpoint",
			"run_id",
			runID,
			"operation_id",
			operation.ID,
			"state",
			operation.State,
		)
	}
}
