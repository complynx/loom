package modules

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/complynx/loom/internal/operations"
	"github.com/complynx/loom/internal/sandbox"
)

const (
	statePrepared         = "prepared"
	statePending          = "pending"
	confirmationServiceID = "service_id"
	jsonNull              = "null"
)

func (r *Registry) decodeEndpointManifest(header http.Header, raw []byte) (Manifest, string, error) {
	manifest, err := r.decodeManifest(raw)
	if err != nil {
		return manifest, "", err
	}
	path := header.Get("Loom-Operation-Inspect-Path")
	if path != "" {
		if err = validPath(path); err != nil {
			return manifest, "", err
		}
	}
	return manifest, path, nil
}

type operationInspection struct {
	ProtocolVersion  int                `json:"protocol_version"`
	Status           string             `json:"status"`
	OperationID      string             `json:"operation_id"`
	ServiceID        string             `json:"service_id"`
	Module           string             `json:"module"`
	Method           string             `json:"method"`
	ContractRevision string             `json:"contract_revision"`
	Binding          operations.Binding `json:"binding"`
	Arguments        json.RawMessage    `json:"arguments"`
	Token            string             `json:"confirmation_id"`
	Preconditions    json.RawMessage    `json:"preconditions"`
	ExpiresAt        time.Time          `json:"expires_at"`
	Description      string             `json:"description"`
}

// Inspect adopts only a service-owned, currently authorized preparation. The
// capability endpoint is read-only; absence never falls back to Prepare.
func (b WriteBackend) Inspect(ctx context.Context, op operations.Operation) (operations.Inspection, error) {
	s, m, principal, err := b.resolve(ctx, op)
	if err != nil {
		return operations.Inspection{}, err
	}
	if s.inspectPath == "" {
		return operations.Inspection{}, operations.ErrAdoptionUnsupported
	}
	payload, err := json.Marshal(map[string]any{
		fieldProtocolVersion: 1, fieldOperationID: op.BackendID(), "module": op.Module, "method": op.Method,
	})
	if err != nil {
		return operations.Inspection{}, err
	}
	raw, err := invoke(ctx, s, principal, s.inspectPath, payload,
		min(m.MaxResultBytes, int64(sandbox.MaxValueBytes)), time.Duration(m.TimeoutMS)*time.Millisecond)
	if err != nil {
		return operations.Inspection{}, err
	}
	var response operationInspection
	if err = json.Unmarshal(raw, &response); err != nil {
		return operations.Inspection{}, err
	}
	if err = validateInspection(op, s, m, response); err != nil {
		return operations.Inspection{}, err
	}
	inspection := operations.Inspection{Preparation: operations.Preparation{
		Arguments: response.Arguments, Description: response.Description, Token: response.Token,
		Preconditions: response.Preconditions, ExpiresAt: response.ExpiresAt,
	}}
	switch response.Status {
	case statePrepared:
		inspection.Result.State = statePrepared
	case stateUnknown:
		inspection.Result.State = stateUnknown
	default:
		inspection.Result, err = b.finishWriteResult(ctx, op, principal, s, m, raw, true)
	}
	return inspection, err
}

func validateInspection(
	op operations.Operation,
	service registered,
	method Method,
	response operationInspection,
) error {
	if response.ProtocolVersion != 1 || response.OperationID != op.BackendID() ||
		response.ServiceID != service.manifest.ServiceID || response.Module != op.Module || response.Method != op.Method ||
		response.ContractRevision != service.manifest.Revision ||
		response.Binding.Issuer != op.Binding.Issuer || response.Binding.Subject != op.Binding.Subject ||
		response.Binding.Actor != op.Binding.Actor || response.Binding.OrgID != op.Binding.OrgID {
		return operations.ErrBinding
	}
	if response.ExpiresAt.IsZero() || len(response.Description) > maxPreparationDescriptionBytes {
		return operations.ErrBinding
	}
	if op.Mode == confirmationServiceID && response.Token == "" {
		return operations.ErrBinding
	}
	if op.Mode == "exact_parameters" &&
		(len(response.Preconditions) == 0 || string(response.Preconditions) == jsonNull) {
		return operations.ErrBinding
	}
	if response.Status != statePrepared && response.Status != stateSucceeded && response.Status != stateUnknown &&
		response.Status != statePending && response.Status != "accepted" && response.Status != stateFailed && response.Status != "cancelled" {
		return operations.ErrBinding
	}
	return validateInspectedArguments(op.Arguments, response.Arguments, method.InputSchema)
}

func validateInspectedArguments(requested, original, schemaJSON json.RawMessage) error {
	if len(original) > maxArgumentBytes {
		return operations.ErrBinding
	}
	var doc any
	if err := decodeJSONDocument(original, &doc); err != nil {
		return err
	}
	if err := validateDocument(doc); err != nil {
		return err
	}
	schema, err := compileSchema(schemaJSON)
	if err != nil {
		return err
	}
	if err = schema.Validate(doc); err != nil {
		return err
	}
	canonical, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	// Empty arguments request inspection of the original terms. Supplied terms
	// must match exactly; adoption cannot amend the native preparation.
	if string(requested) != "{}" && !bytes.Equal(requested, canonical) {
		return operations.ErrBinding
	}
	return nil
}
