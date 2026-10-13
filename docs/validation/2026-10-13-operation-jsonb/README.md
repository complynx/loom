# Operation JSONB binding acceptance

GitHub issue: https://github.com/complynx/loom/issues/3

## Cause and fix

PostgreSQL JSONB reorders object keys. json.Marshal preserves the order inside
json.RawMessage. The saved request and preparation therefore produced a different
binding seal after a database read.

Seal verification now normalizes request arguments, prepared arguments and
preconditions with the same UseNumber decoder used during preparation. The outer
struct encoding stays unchanged, so existing canonical key-order seals remain
valid. Duplicate preparation also normalizes the saved request before comparison.
Changed parameters, authority and preconditions still fail closed. No intent is
resealed in the database. No schema migration is required.

## Verified

- Baseline: the new real-PostgreSQL test failed in all three confirmation modes.
- After fix: none, service_id and exact_parameters survive JSONB key reordering,
  including nested objects, arrays and integers greater than 2^53.
- Preparing the same request returns the same operation ID. A changed integer is
  rejected without replacing the operation.
- Approval and dispatch survive the database round trip. Repeated dispatch calls
  execute the backend exactly once.
- Changes to stored request arguments, prepared arguments or preconditions are
  rejected at both approval and dispatch. The backend receives no call.
- Full Windows integration suite passed on an isolated PostgreSQL database.
- Full Linux Docker race/integration suite passed; Golden lint reported 0 issues.
- Independent diff review: one pass, no actionable findings in this scope.

## Limits and recovery

This fixes object key ordering; it is not a full numeric canonicalization scheme.
UseNumber preserves number spelling. PostgreSQL can rewrite exponent and negative
zero forms, so these forms can still cause fail-closed binding rejection. Use plain
decimal numbers without negative zero in write arguments and preconditions.
A later numeric-normalization change needs an explicit seal compatibility plan.

An existing undispatched operation affected only by key ordering can use its
original operation ID and binding hash after upgrading. Expiry, current authority
and run lease checks still apply. Unknown or dispatching writes must be reconciled;
do not create a replacement operation to bypass an uncertain outcome.

The actual Shuttle registration flow has not been repeated with this patch.
These tests exercise Loom's real coordinator and PostgreSQL, with a fake backend;
no production changes or paid provider calls were made.

Written by Codex (gpt-6/Codex desktop), developer of Loom
on behalf of Daniel Drizhuk
