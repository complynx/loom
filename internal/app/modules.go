package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/complynx/clx-scripter/internal/modules"
	"github.com/complynx/clx-scripter/internal/operations"
	"github.com/complynx/clx-scripter/internal/provider"
	"github.com/complynx/clx-scripter/internal/runs"
	"github.com/complynx/clx-scripter/internal/store"
	"github.com/jackc/pgx/v5"
)

type RunAuthorizer func(context.Context, domain.Run) (domain.Principal, string, error)

// ModuleBridge is the production binding between fenced runs and durable writes.
// It exposes no credentials or direct mutation path to JavaScript.
type ModuleBridge struct {
	registry    *modules.Registry
	coordinator *operations.Coordinator
	repository  *store.Store
	authorize   RunAuthorizer
}

func NewModuleBridge(registry *modules.Registry, coordinator *operations.Coordinator, repository *store.Store, authorize RunAuthorizer) *ModuleBridge {
	return &ModuleBridge{registry, coordinator, repository, authorize}
}
func modulePrincipal(p domain.Principal, token string, revision int64) modules.Principal {
	permissions := []string{}
	for permission, enabled := range p.Permissions {
		if enabled {
			permissions = append(permissions, permission)
		}
	}
	return modules.Principal{Issuer: p.Issuer, Subject: p.Subject, Actor: p.Actor, Organization: p.OrgID, Permissions: permissions, Token: token, PolicyRevision: strconv.FormatInt(revision, 10)}
}
func (b *ModuleBridge) guard(ctx context.Context, run domain.Run) error {
	current, err := b.repository.ExecutionRun(ctx, run.ID)
	if err != nil {
		return err
	}
	if current.State != domain.Running || current.LeaseOwner != run.LeaseOwner || current.Fence != run.Fence || current.PauseRequested || current.CancelRequested {
		return operations.ErrLease
	}
	if current.LeaseUntil == nil || !current.LeaseUntil.After(time.Now()) {
		return operations.ErrLease
	}
	_, _, err = b.authorize(ctx, current)
	return err
}

func (b *ModuleBridge) Call(ctx context.Context, run domain.Run, p domain.Principal, token string, call provider.Call) (json.RawMessage, *domain.Approval, error) {
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
	if descriptor.Method.Effect == "read" {
		return json.RawMessage(`{"error":"use_script_for_reads"}`), nil, nil
	}
	backend := modules.WriteBackend{Registry: b.registry, Principal: mp, RefreshPrincipal: func(c context.Context) (modules.Principal, error) {
		fresh, credential, e := b.authorize(c, run)
		value := modulePrincipal(fresh, credential, run.SettingsSnapshot.Version)
		value.UserID = run.OwnerID
		return value, e
	}, Guard: func(c context.Context) error { return b.guard(c, run) }}
	request := operations.Request{RunID: run.ID, CallID: call.ID, Module: args.Module, Method: args.Method, Arguments: args.Arguments, Mode: descriptor.Method.Confirmation.Mode, Binding: operations.Binding{UserID: run.OwnerID, Issuer: p.Issuer, Subject: p.Subject, Actor: p.Actor, OrgID: p.OrgID, ContractHash: descriptor.ContractHash}}
	operation, err := b.coordinator.Prepare(ctx, request, backend)
	if err != nil {
		return nil, nil, err
	}
	if operation.State == "prepared" {
		text := operation.Preparation.Description
		if text == "" {
			text = fmt.Sprintf("Confirm %s.%s", args.Module, args.Method)
		}
		approval := &domain.Approval{ID: operation.ID, OperationID: operation.ID, Method: args.Module + "." + args.Method, Arguments: operation.Preparation.Arguments, ArgumentHash: operation.BindingHash, Text: text, ExpiresAt: operation.Preparation.ExpiresAt}
		return nil, approval, nil
	}
	if operation.State == "approved" {
		operation, err = b.coordinator.Dispatch(ctx, operation.ID, operations.Lease{Owner: run.LeaseOwner, Fence: run.Fence}, backend.Authorize, backend)
		if err != nil {
			if operation.State == "unknown" {
				return nil, nil, &runs.Suspension{State: domain.Reconciling, Reason: "operation_unknown"}
			}
			return nil, nil, err
		}
	}
	if operation.State == "unknown" || operation.State == "pending" || operation.State == "dispatching" {
		operation, err = b.coordinator.Reconcile(ctx, operation.ID, backend)
		if err != nil {
			return nil, nil, &runs.Suspension{State: domain.Reconciling, Reason: "operation_unknown"}
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
		return nil, nil, &runs.Suspension{State: domain.Reconciling, Reason: "operation_unknown"}
	}
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
	if operation.RunID != run.ID || operation.Binding.UserID != run.OwnerID || operation.Binding.Actor != run.ActorID || operation.Binding.OrgID != run.OrgID || operation.BindingHash != run.Approval.ArgumentHash {
		return operations.ErrBinding
	}
	if input.Decision != "approve" && input.Decision != "reject" {
		return errors.New("invalid approval decision")
	}
	_, err = b.coordinator.ApproveTx(ctx, tx, operation.ID, operations.Approval{Binding: operation.Binding, BindingHash: run.Approval.ArgumentHash, Approve: input.Decision == "approve"})
	return err
}

// Discard cancels only actions proven not to have dispatched. Ambiguous writes
// are resolved with status lookup; steering never replays or forgets an effect.
func (b *ModuleBridge) Discard(ctx context.Context, run domain.Run, p domain.Principal, token string, call provider.Call) (json.RawMessage, *domain.Approval, error) {
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
	if op.Binding.UserID != run.OwnerID || op.Binding.Issuer != fresh.Issuer || op.Binding.Subject != fresh.Subject || op.Binding.Actor != fresh.Actor || op.Binding.OrgID != fresh.OrgID {
		return nil, nil, operations.ErrBinding
	}
	op, proven, err := b.coordinator.CancelUndispatched(ctx, op.ID, operations.Lease{Owner: run.LeaseOwner, Fence: run.Fence}, op.Binding)
	if err != nil {
		return nil, nil, err
	}
	if proven {
		return cancelled, nil, nil
	}
	mp := modulePrincipal(fresh, credential, run.SettingsSnapshot.Version)
	mp.UserID = run.OwnerID
	backend := modules.WriteBackend{Registry: b.registry, Principal: mp, RefreshPrincipal: func(c context.Context) (modules.Principal, error) {
		user, secret, e := b.authorize(c, run)
		value := modulePrincipal(user, secret, run.SettingsSnapshot.Version)
		value.UserID = run.OwnerID
		return value, e
	}, Guard: func(c context.Context) error { return b.guard(c, run) }}
	if op.State == "dispatching" || op.State == "pending" || op.State == "unknown" {
		op, err = b.coordinator.Reconcile(ctx, op.ID, backend)
		if err != nil {
			return nil, nil, &runs.Suspension{State: domain.Reconciling, Reason: "operation_unknown"}
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
		return nil, nil, &runs.Suspension{State: domain.Reconciling, Reason: "operation_unknown"}
	}
}
