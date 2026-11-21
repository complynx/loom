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
	SupportsOperationAdoption bool
	ServiceID, ContractHash   string
	Method                    Method
}

// Describe resolves the current permission-filtered contract for a structured write tool.
func (r *Registry) Describe(ctx context.Context, p Principal, module, method string) (Descriptor, error) {
	s, m, err := r.lookup(ctx, p, module, method)
	if err != nil {
		return Descriptor{}, err
	}
	return methodDescriptor(s, module, m), nil
}

// DescribeFresh checks current method access without reusing dynamic discovery.
// Receipt disclosure uses this check without authorizing an old write contract.
func (r *Registry) DescribeFresh(ctx context.Context, p Principal, module, method string) (Descriptor, error) {
	s, m, err := r.lookupFresh(ctx, p, module, method)
	if err != nil {
		return Descriptor{}, err
	}
	return methodDescriptor(s, module, m), nil
}

func methodDescriptor(s registered, module string, m Method) Descriptor {
	return Descriptor{
		ServiceID:                 s.manifest.ServiceID,
		ContractHash:              contractHash(s, module, m),
		Method:                    m,
		SupportsOperationAdoption: s.inspectPath != "",
	}
}

// DescribeExternal resolves the contract of a read method marked for external agents.
// Other methods are reported as unavailable.
func (r *Registry) DescribeExternal(ctx context.Context, p Principal, module, method string) (Descriptor, error) {
	descriptor, err := r.Describe(ctx, p, module, method)
	if err != nil {
		return Descriptor{}, err
	}
	if !descriptor.Method.ExternalAgent || descriptor.Method.Effect != effectRead {
		return Descriptor{}, ErrUnavailable
	}
	return descriptor, nil
}

func externalWrite(method Method) bool {
	return method.ExternalAgent && method.Effect != effectRead && method.Idempotency &&
		method.StatusPath != "" && len(method.ArtifactMIMETypes) == 0
}

func (r *Registry) DescribeExternalAction(
	ctx context.Context,
	p Principal,
	service, module, method string,
) (Descriptor, error) {
	s, m, err := r.externalWriteMethod(ctx, p, service, module, method)
	if err != nil {
		return Descriptor{}, err
	}
	return methodDescriptor(s, module, m), nil
}

func (r *Registry) externalWriteMethod(
	ctx context.Context, p Principal, service, module, method string,
) (registered, Method, error) {
	if service == "" {
		return registered{}, Method{}, ErrUnavailable
	}
	s, m, err := r.lookupWithFresh(ctx, p, service, module, method, true)
	if err == nil && !externalWrite(m) {
		err = ErrUnavailable
	}
	return s, m, err
}

// WriteBackend is used only by operations.Coordinator, never a JavaScript proxy.
type WriteBackend struct {
	Registry         *Registry
	Principal        Principal
	RefreshPrincipal func(context.Context) (Principal, error)
	Guard            func(context.Context) error
	ExternalWriteKey string
}

func (b WriteBackend) Authorize(ctx context.Context, op operations.Operation) error {
	_, _, _, err := b.resolve(ctx, op)
	return err
}

// ReadExternalReceipt checks current method access. For a data-bearing receipt,
// the service must also authorize the specific operation before returning data.
func (b WriteBackend) ReadExternalReceipt(ctx context.Context, op operations.Operation) (json.RawMessage, error) {
	s, m, p, err := b.externalStatusAuthority(ctx, op)
	if err != nil || len(op.Result) == 0 {
		return nil, err
	}
	result, err := b.readStatus(ctx, op, s, m, p)
	if err != nil {
		return nil, err
	}
	if result.State != stateSucceeded {
		return nil, ErrUnavailable
	}
	return result.Output, nil
}

func (b WriteBackend) externalStatusAuthority(
	ctx context.Context, op operations.Operation,
) (registered, Method, Principal, error) {
	if op.ExternalSessionID == "" || b.ExternalWriteKey == "" {
		return registered{}, Method{}, Principal{}, operations.ErrBinding
	}
	p, err := b.boundPrincipal(ctx, op)
	if err != nil {
		return registered{}, Method{}, Principal{}, err
	}
	s, m, err := b.Registry.externalWriteMethod(ctx, p, op.ExternalService, op.Module, op.Method)
	p.ExternalWriteKey = b.ExternalWriteKey
	return s, m, p, err
}

func (b WriteBackend) boundPrincipal(ctx context.Context, op operations.Operation) (Principal, error) {
	if b.Guard != nil {
		if err := b.Guard(ctx); err != nil {
			return Principal{}, err
		}
	}
	p := b.Principal
	if b.RefreshPrincipal != nil {
		fresh, err := b.RefreshPrincipal(ctx)
		if err != nil {
			return Principal{}, err
		}
		p = fresh
	}
	if p.Issuer != op.Binding.Issuer || p.Subject != op.Binding.Subject || p.Organization != op.Binding.OrgID ||
		p.Actor != op.Binding.Actor {
		return Principal{}, operations.ErrBinding
	}
	return p, nil
}

func (b WriteBackend) resolve(ctx context.Context, op operations.Operation) (registered, Method, Principal, error) {
	p, err := b.boundPrincipal(ctx, op)
	if err != nil {
		return registered{}, Method{}, Principal{}, err
	}
	var s registered
	var m Method
	if op.ExternalSessionID == "" {
		s, m, err = b.Registry.lookupFresh(ctx, p, op.Module, op.Method)
	} else {
		s, m, err = b.Registry.externalWriteMethod(ctx, p, op.ExternalService, op.Module, op.Method)
	}
	if err != nil {
		return registered{}, Method{}, Principal{}, err
	}
	if m.Effect == effectRead || contractHash(s, op.Module, m) != op.Binding.ContractHash ||
		m.Confirmation.Mode != op.Mode {
		return registered{}, Method{}, Principal{}, operations.ErrBinding
	}
	if op.ExternalSessionID != "" {
		if b.ExternalWriteKey == "" || m.ExternalAgentProtectedArguments != (len(op.ProtectedArguments) > 0) {
			return registered{}, Method{}, Principal{}, operations.ErrBinding
		}
		p.ExternalWriteKey = b.ExternalWriteKey
	}
	if op.BackendOperationID != "" {
		return s, m, p, nil
	}
	err = validateWriteArguments(m, op.Arguments)
	return s, m, p, err
}

func validateWriteArguments(m Method, raw json.RawMessage) error {
	input, err := compileSchema(m.InputSchema)
	if err != nil {
		return err
	}
	var doc any
	if err = decodeJSONDocument(raw, &doc); err != nil {
		return &InvalidArgumentsError{Reason: "invalid_json"}
	}
	if len(raw) > maxArgumentBytes {
		return &InvalidArgumentsError{Reason: "too_large"}
	}
	if err = validateDocument(doc); err != nil {
		return &InvalidArgumentsError{Reason: "too_complex"}
	}
	if err = input.Validate(doc); err != nil {
		return argumentSchemaError(input, err)
	}
	return nil
}
func (b WriteBackend) Prepare(ctx context.Context, op operations.Operation) (operations.Preparation, error) {
	s, m, principal, err := b.resolve(ctx, op)
	if err != nil {
		return operations.Preparation{}, err
	}
	if op.Mode == noConfirmation {
		return operations.Preparation{ExpiresAt: time.Now().UTC().Add(defaultPreparationTTL)}, nil
	}
	envelope, _ := json.Marshal(
		map[string]any{
			fieldProtocolVersion: 1,
			fieldOperationID:     op.BackendID(),
			fieldRunID:           operationContextID(op),
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
	if response.Status != statePrepared || response.OperationID != op.ID || !response.ExpiresAt.After(time.Now()) ||
		len(response.Description) > maxPreparationDescriptionBytes {
		return operations.Preparation{}, operations.ErrBinding
	}
	if op.ExternalSessionID != "" && response.Description == "" {
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
	if op.Mode == confirmationServiceID && response.Token == "" {
		return operations.Preparation{}, operations.ErrBinding
	}
	if op.Mode == "exact_parameters" &&
		(len(response.Arguments) == 0 || len(response.Preconditions) == 0 || string(response.Preconditions) == jsonNull) {
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
			fieldOperationID:     op.BackendID(),
			fieldRunID:           operationContextID(op),
			fieldArguments:       arguments,
			"idempotency_key":    op.BackendID(),
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
	resolve := b.resolve
	if op.ExternalSessionID != "" {
		// Status never dispatches. Use the current service and original operation ID.
		resolve = b.externalStatusAuthority
	}
	s, m, principal, err := resolve(ctx, op)
	if err != nil {
		return operations.Result{}, err
	}
	return b.readStatus(ctx, op, s, m, principal)
}

func (b WriteBackend) readStatus(
	ctx context.Context, op operations.Operation, s registered, m Method, principal Principal,
) (operations.Result, error) {
	payload, _ := json.Marshal(map[string]any{fieldProtocolVersion: 1, fieldOperationID: op.BackendID()})
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
	case "accepted", statePending:
		return operations.Result{State: statePending}, nil
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
			confirmationServiceID: service.manifest.ServiceID,
			"module":              module,
			"manifest_hash":       service.hash,
			"method":              method,
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
	result, err := decodeWriteResult(raw, op.BackendID(), m.OutputSchema, status)
	if err != nil || result.State != stateSucceeded || op.ExternalSessionID != "" {
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

func operationContextID(op operations.Operation) string {
	if op.ExternalSessionID != "" {
		return op.ID
	}
	return op.RunID
}
