package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/modules"
	"github.com/complynx/loom/internal/operations"
	"github.com/complynx/loom/internal/runs"
)

func TestRecoveryReceiptsShareLookupButCheckEveryIdentity(t *testing.T) {
	t.Parallel()
	p := domain.Principal{Issuer: "issuer", Subject: "owner", Actor: "actor", OrgID: "org"}
	r := domain.Run{OwnerID: "owner-id"}
	binding := operations.Binding{
		UserID:       r.OwnerID,
		Issuer:       p.Issuer,
		Subject:      p.Subject,
		Actor:        p.Actor,
		OrgID:        p.OrgID,
		ContractHash: "contract",
	}
	request := operations.Request{
		RunID:     "prior",
		CallID:    "call-1",
		Module:    "booking",
		Method:    "book",
		Arguments: json.RawMessage(`{"slot_id":"requested"}`),
		Binding:   binding,
		Mode:      "service_id",
	}
	first := operations.Operation{ID: "one", State: "succeeded", Request: request}
	first.Preparation = operations.Preparation{
		Arguments: json.RawMessage(`{"slot_id":"resolved"}`),
		Token:     "never-copy-confirmation",
	}
	second := first
	second.ID = "two"
	second.CallID = "call-2"
	calls := 0
	describe := func(context.Context, operations.Operation) (modules.Descriptor, error) {
		calls++
		d := modules.Descriptor{ContractHash: "new-contract"}
		d.Method.Confirmation.Mode = "service_id"
		return d, nil
	}
	receipts, err := authorizedRecoveryReceipts(t.Context(), r, p, []operations.Operation{first, second}, describe)
	require.NoError(t, err)
	require.Len(t, receipts, 2)
	require.Equal(t, 1, calls)
	require.True(t, receipts[0].ContractChanged)
	require.JSONEq(t, `{"slot_id":"resolved"}`, string(receipts[0].Arguments))
	require.JSONEq(t, `{"slot_id":"requested"}`, string(receipts[0].RequestedArguments))
	items, err := boundedRecoveryReceiptItems(receipts)
	require.NoError(t, err)
	wire, err := json.Marshal(items)
	require.NoError(t, err)
	require.NotContains(t, string(wire), first.Preparation.Token)
	var call struct {
		Arguments string `json:"arguments"`
	}
	require.NoError(t, json.Unmarshal(items[1], &call))
	require.JSONEq(t, `{"module":"booking","method":"book","arguments":{"slot_id":"resolved"}}`, call.Arguments)
	second.Binding.Actor = "foreign"
	receipts, err = authorizedRecoveryReceipts(t.Context(), r, p, []operations.Operation{first, second}, describe)
	require.ErrorIs(t, err, operations.ErrBinding)
	require.Empty(t, receipts)
	require.Equal(t, 2, calls)
	failure := errors.New("temporary discovery failure")
	_, err = authorizedRecoveryReceipts(
		t.Context(),
		r,
		p,
		[]operations.Operation{first},
		func(context.Context, operations.Operation) (modules.Descriptor, error) {
			return modules.Descriptor{}, failure
		},
	)
	require.ErrorIs(t, err, failure)
	// A hidden or removed module is an explicit stop, not a retryable failure.
	_, err = authorizedRecoveryReceipts(
		t.Context(),
		r,
		p,
		[]operations.Operation{first},
		func(context.Context, operations.Operation) (modules.Descriptor, error) {
			return modules.Descriptor{}, modules.ErrUnavailable
		},
	)
	var suspension *runs.Suspension
	require.ErrorAs(t, err, &suspension)
	require.Equal(t, domain.Paused, suspension.State)
	require.Equal(t, "recovery_context_unavailable", suspension.Reason)
}
