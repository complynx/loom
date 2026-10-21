package modules

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"time"

	"github.com/complynx/loom/internal/operations"
	"github.com/complynx/loom/internal/sandbox"
)

const maxPreparationDescriptionBytes = 4000

type Descriptor struct {
	ServiceID, ContractHash string
	Method                  Method
}

// Describe resolves the current permission-filtered contract for a structured write tool.
func (r *Registry) Describe(ctx context.Context, p Principal, module, method string) (Descriptor, error) {
	s, m, err := r.lookup(ctx, p, module, method)
	if err != nil {
		return Descriptor{}, err
	}
	return Descriptor{ServiceID: s.manifest.ServiceID, ContractHash: contractHash(s, module, m), Method: m}, nil
}

// WriteBackend is used only by operations.Coordinator, never a JavaScript proxy.
type WriteBackend struct {
	Registry         *Registry
	Principal        Principal
	RefreshPrincipal func(context.Context) (Principal, error)
	Guard            func(context.Context) error
}

func (b WriteBackend) Authorize(ctx context.Context, op operations.Operation) error {
	_, _, _, err := b.resolve(ctx, op)
	return err
}
func (b WriteBackend) resolve(ctx context.Context, op operations.Operation) (registered, Method, Principal, error) {
	if b.Guard != nil {
		if err := b.Guard(ctx); err != nil {
			return registered{}, Method{}, Principal{}, err
		}
	}
	p := b.Principal
	if b.RefreshPrincipal != nil {
		fresh, err := b.RefreshPrincipal(ctx)
		if err != nil {
			return registered{}, Method{}, Principal{}, err
		}
		p = fresh
	}
	if p.Issuer != op.Binding.Issuer || p.Subject != op.Binding.Subject || p.Organization != op.Binding.OrgID ||
		p.Actor != op.Binding.Actor {
		return registered{}, Method{}, Principal{}, operations.ErrBinding
	}
	s, m, err := b.Registry.lookupFresh(ctx, p, op.Module, op.Method)
	if err != nil {
		return registered{}, Method{}, Principal{}, err
	}
	if m.Effect == effectRead || contractHash(s, op.Module, m) != op.Binding.ContractHash ||
		m.Confirmation.Mode != op.Mode {
		return registered{}, Method{}, Principal{}, operations.ErrBinding
	}
	input, err := compileSchema(m.InputSchema)
	if err != nil {
		return registered{}, Method{}, Principal{}, err
	}
	var doc any
	if err = decodeJSONDocument(op.Arguments, &doc); err != nil {
		return registered{}, Method{}, Principal{}, err
	}
	if len(op.Arguments) > maxArgumentBytes {
		return registered{}, Method{}, Principal{}, errors.New("module arguments exceed 65536 bytes")
	}
	if err = validateDocument(doc); err != nil {
		return registered{}, Method{}, Principal{}, err
	}
	if err = input.Validate(doc); err != nil {
		return registered{}, Method{}, Principal{}, err
	}
	return s, m, p, nil
}
func (b WriteBackend) Prepare(ctx context.Context, op operations.Operation) (operations.Preparation, error) {
	s, m, principal, err := b.resolve(ctx, op)
	if err != nil {
		return operations.Preparation{}, err
	}
	if op.Mode == "none" {
		return operations.Preparation{ExpiresAt: time.Now().UTC().Add(defaultPreparationTTL)}, nil
	}
	envelope, _ := json.Marshal(
		map[string]any{
			fieldProtocolVersion: 1,
			fieldOperationID:     op.ID,
			fieldRunID:           op.RunID,
			fieldArguments:       op.Arguments,
		},
	)
	raw, err := invoke(
		ctx,
		s,
		principal,
		m.Confirmation.PreparePath,
		envelope,
		min(m.MaxResultBytes, int64(sandbox.MaxValueBytes)),
		time.Duration(m.TimeoutMS)*time.Millisecond,
	)
	if err != nil {
		return operations.Preparation{}, err
	}
	var response struct {
		Status        string          `json:"status"`
		OperationID   string          `json:"operation_id"`
		Token         string          `json:"confirmation_id"`
		Arguments     json.RawMessage `json:"arguments"`
		Preconditions json.RawMessage `json:"preconditions"`
		ExpiresAt     time.Time       `json:"expires_at"`
		Description   string          `json:"description"`
	}
	if err = json.Unmarshal(raw, &response); err != nil {
		return operations.Preparation{}, err
	}
	if response.Status != "prepared" || response.OperationID != op.ID || !response.ExpiresAt.After(time.Now()) ||
		len(response.Description) > maxPreparationDescriptionBytes {
		return operations.Preparation{}, operations.ErrBinding
	}
	preparedArgs := response.Arguments
	if len(preparedArgs) == 0 {
		preparedArgs = op.Arguments
	}
	schema, err := compileSchema(m.InputSchema)
	if err != nil {
		return operations.Preparation{}, err
	}
	var preparedDoc any
	if err = decodeJSONDocument(preparedArgs, &preparedDoc); err != nil {
		return operations.Preparation{}, err
	}
	if len(preparedArgs) > maxArgumentBytes {
		return operations.Preparation{}, errors.New("prepared arguments too large")
	}
	if err = validateDocument(preparedDoc); err != nil {
		return operations.Preparation{}, err
	}
	if err = schema.Validate(preparedDoc); err != nil {
		return operations.Preparation{}, err
	}
	if op.Mode == "service_id" && response.Token == "" {
		return operations.Preparation{}, operations.ErrBinding
	}
	if op.Mode == "exact_parameters" &&
		(len(response.Arguments) == 0 || len(response.Preconditions) == 0 || string(response.Preconditions) == "null") {
		return operations.Preparation{}, operations.ErrBinding
	}
	return operations.Preparation{
		Arguments:     preparedArgs,
		Description:   response.Description,
		Token:         response.Token,
		Preconditions: response.Preconditions,
		ExpiresAt:     response.ExpiresAt,
	}, nil
}

//nolint:nonamedreturns // The defer marks only failures before the execute RPC.
func (b WriteBackend) Execute(ctx context.Context, op operations.Operation) (result operations.Result, err error) {
	dispatched := false
	defer func() {
		if err != nil && !dispatched {
			err = &operations.NotDispatchedError{Cause: err}
		}
	}()
	s, m, principal, err := b.resolve(ctx, op)
	if err != nil {
		return operations.Result{}, err
	}
	if !op.Preparation.ExpiresAt.After(time.Now()) {
		return operations.Result{}, operations.ErrExpired
	}
	arguments := op.Preparation.Arguments
	if len(arguments) == 0 {
		arguments = op.Arguments
	}
	schema, err := compileSchema(m.InputSchema)
	if err != nil {
		return operations.Result{}, err
	}
	var argumentDoc any
	if err = decodeJSONDocument(arguments, &argumentDoc); err != nil {
		return operations.Result{}, err
	}
	if len(arguments) > maxArgumentBytes {
		return operations.Result{}, errors.New("prepared arguments too large")
	}
	if err = validateDocument(argumentDoc); err != nil {
		return operations.Result{}, err
	}
	if err = schema.Validate(argumentDoc); err != nil {
		return operations.Result{}, err
	}
	confirmation := map[string]any{"mode": op.Mode}
	if op.Preparation.Token != "" {
		confirmation["confirmation_id"] = op.Preparation.Token
	}
	if len(op.Preparation.Preconditions) > 0 {
		confirmation["preconditions"] = op.Preparation.Preconditions
	}
	envelope, _ := json.Marshal(
		map[string]any{
			fieldProtocolVersion: 1,
			fieldOperationID:     op.ID,
			fieldRunID:           op.RunID,
			fieldArguments:       arguments,
			"idempotency_key":    op.ID,
			"confirmation":       confirmation,
		},
	)
	if b.Guard != nil {
		if err = b.Guard(ctx); err != nil {
			return operations.Result{}, err
		}
	}
	dispatched = true
	raw, err := invoke(
		ctx,
		s,
		principal,
		m.Path,
		envelope,
		min(m.MaxResultBytes, int64(sandbox.MaxValueBytes)),
		time.Duration(m.TimeoutMS)*time.Millisecond,
	)
	if err != nil {
		return operations.Result{}, err
	}
	return b.finishWriteResult(ctx, op, principal, s, m, raw, false)
}
func (b WriteBackend) Status(ctx context.Context, op operations.Operation) (operations.Result, error) {
	s, m, principal, err := b.resolve(ctx, op)
	if err != nil {
		return operations.Result{}, err
	}
	payload, _ := json.Marshal(map[string]any{fieldProtocolVersion: 1, fieldOperationID: op.ID})
	raw, err := invoke(
		ctx,
		s,
		principal,
		m.StatusPath,
		payload,
		min(m.MaxResultBytes, int64(sandbox.MaxValueBytes)),
		time.Duration(m.TimeoutMS)*time.Millisecond,
	)
	if err != nil {
		return operations.Result{}, err
	}
	return b.finishWriteResult(ctx, op, principal, s, m, raw, true)
}

func decodeWriteResult(
	raw json.RawMessage,
	id string,
	outputSchema json.RawMessage,
	statusLookup bool,
) (operations.Result, error) {
	var response struct {
		Status      string          `json:"status"`
		OperationID string          `json:"operation_id"`
		Result      json.RawMessage `json:"result"`
		Artifacts   json.RawMessage `json:"artifacts"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return operations.Result{}, err
	}
	if response.OperationID != id {
		return operations.Result{State: stateUnknown}, operations.ErrBinding
	}
	switch response.Status {
	case stateSucceeded:
		// Unsupported attachments must not erase knowledge that the write succeeded.
		schema, err := compileSchema(outputSchema)
		if err != nil {
			return operations.Result{State: stateUnknown}, err
		}
		var doc any
		if err = decodeJSONDocument(response.Result, &doc); err != nil {
			return operations.Result{State: stateUnknown}, err
		}
		if err = validateDocument(doc); err != nil {
			return operations.Result{State: stateUnknown}, err
		}
		if err = schema.Validate(doc); err != nil {
			return operations.Result{State: stateUnknown}, err
		}
		return operations.Result{State: stateSucceeded, Output: response.Result}, nil
	case "accepted", "pending":
		return operations.Result{State: "pending"}, nil
	case "rejected":
		if !statusLookup {
			return operations.Result{State: stateFailed}, nil
		}
	case stateFailed, "cancelled":
		if statusLookup {
			return operations.Result{State: stateFailed}, nil
		}
	}
	// not_found cannot prove that a delayed prior execution will never commit.
	return operations.Result{State: stateUnknown}, nil
}

// Contract hashes cover user-specific resolved methods as well as the static manifest.
func contractHash(service registered, module string, method Method) string {
	raw, _ := json.Marshal(
		map[string]any{
			"service_id":    service.manifest.ServiceID,
			"module":        module,
			"manifest_hash": service.hash,
			"method":        method,
		},
	)
	var document any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	_ = decoder.Decode(&document)
	canonical, _ := json.Marshal(document)
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func (b WriteBackend) finishWriteResult(
	ctx context.Context,
	op operations.Operation,
	p Principal,
	s registered,
	m Method,
	raw json.RawMessage,
	status bool,
) (operations.Result, error) {
	result, err := decodeWriteResult(raw, op.ID, m.OutputSchema, status)
	if err != nil || result.State != stateSucceeded {
		return result, err
	}
	var response struct {
		Artifacts json.RawMessage `json:"artifacts"`
	}
	_ = json.Unmarshal(raw, &response) // decodeWriteResult already checked this JSON.
	imported, importErr := b.Registry.importArtifacts(
		ctx,
		p,
		s,
		op.Module,
		op.RunID,
		m,
		response.Artifacts,
		b.Guard,
		b.RefreshPrincipal,
	)
	if importErr != nil {
		// An unavailable attachment must never turn a known successful mutation into a retry.
		result.Output, _ = json.Marshal(
			map[string]any{
				fieldResult:             result.Output,
				fieldArtifacts:          []any{},
				"artifact_import_error": "artifact_unavailable",
			},
		)
	} else if len(imported) > 0 {
		result.Output, _ = json.Marshal(map[string]any{fieldResult: result.Output, fieldArtifacts: imported})
	}
	return result, nil
}
