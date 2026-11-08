# Monthly wallet funding

Monthly funding adds credit to the existing owner wallet. It does not add a spending
cap, change run budgets, release held funds or grant permissions. It is disabled
when settings.monthly_funding is absent or enabled is false.

## Amounts and administration

Authorized chat administration uses the generated
[administration skill and tools](administration-tools.md). A bot administrator
role alone does not grant these Loom permissions.

Settings use integer microcredits. The audited OpenAI preset uses 1 USD =
1,000,000 microcredits; other tariff configurations need not use that conversion.
Factors use integer millionths: 1000000 means 1, 1500000 means 1.5. Zero is allowed.
Negative values and an effective target above signed 64-bit range are rejected.
The target rounds down once to a whole microcredit after both factors:

```text
target = floor((owner.base_microcredits ?? defaults.base_microcredits)
               * defaults.factor_ppm * (owner.factor_ppm ?? 1000000) / 1000000000000)
credit = max(0, target - wallet.balance)
```

wallet.balance includes reservations. Do not subtract reserved funds before this
calculation. Balance above target is retained. Manual grants remain available.

GET/PATCH /v1/admin/settings requires settings.read/settings.write and the configured
administrative organization. Preserve the full settings body and current version
when updating. Set monthly_funding to, for example:

```json
{"enabled":true,"base_microcredits":1000000,"factor_ppm":1000000}
```

GET/PATCH /v1/admin/billing/{owner_id}/monthly-funding requires billing.adjust and
a target membership in the administrator's organization. The wallet and its funding
policy belong to the owner, shared across that owner's memberships, like existing
credit adjustments. There is one grant per owner/month, not one per organization.
PATCH replaces the funding policy and requires its current version (0 to create):

```json
{"enabled":null,"base_microcredits":null,"factor_ppm":2000000,"version":0}
```

enabled null inherits participation for a known ordinary user; false opts out;
true explicitly opts in, including an administrator-approved service or unknown
historical account. The global switch must also be enabled. Null base inherits
the global base; null personal factor means 1. The global factor always applies.
Policy changes use optimistic versions and write audit events. Ordinary users
cannot edit funding through preferences. A conflicting version requires a reload.
Use the existing idempotent billing adjustment endpoint for immediate manual funds.

GET/PATCH /v1/admin/settings/monthly-funding edits only funding defaults, using
{defaults:{enabled,base_microcredits,factor_ppm},version}. This endpoint uses the
same settings.read/write authority and global settings version without returning
unrelated numeric settings through JavaScript.

The operator panel has Defaults and Owner policy forms. It shows effective target,
identity classification, next due time and last receipt. It needs the existing
diagnostics login permission plus the settings or billing scopes above. SSO proxy
funding PATCHes require the exact configured Origin and X-Loom-Admin-Request: 1;
only settings/monthly-funding and the exact owner monthly-funding route permit browser writes.
Other operator routes remain read-only. Browser numeric inputs accept only nonnegative safe integers (up to 9007199254740991);
use the API for larger int64 amounts. Lost responses are not automatically
retried: reload and verify the version before saving again.

## Calendar, activation and receipts

A background sweep runs on startup and each minute, processing at most 100 wallets
per pass. Inactive participating wallets do not need a model run. Wallet resolution (for example, a balance read) complements this sweep in a
separate short transaction. A new participating wallet is funded atomically at
creation; existing run/chat mutation transactions never hold refill locks.
The database clock defines calendar months in UTC. An outage funds only the current
month on recovery, never intermediate missed months or periods before activation.
With more than 100 due wallets, subsequent passes continue with unprocessed owners.

A new participating wallet receives its target through monthly funding instead
of the separate initial credit. Its initial ledger entry records zero. If a
positive initial grant already funded the current month before activation, that
month gets a zero receipt with reason initial_credit_period, and monthly funding
starts next month. This prevents topping up already spent initial credit. The
receipt still records the effective target and balance. Existing non-participants
retain the initial-credit behavior.

Historical wallets have unknown service classification after migration. They are
excluded by default until a wallet-resolution request classifies the identity
(no model run required), or billing.adjust explicitly opts them in. Known service
accounts remain excluded unless explicitly enabled. A previously observed service
classification is never downgraded by an ordinary request. Funding never changes
the payer of a descriptor or other service.

Each atomic wallet transaction stores a durable owner+UTC-period receipt, including
zero credit, with target, balance used, credited amount, global settings version,
effective defaults/owner policy and timestamp. The credit ledger records the same
grant. Workers, retries and restarts serialize on the wallet and cannot grant twice.
Reserve, settlement and manual adjustment use the same wallet row. Reservations
are neither reset nor treated as spent.

Changes apply to future unprocessed periods. A completed period, its policy snapshot
and receipt are never recalculated, including after spending, setting changes,
disable/re-enable or a restart.

GET /v1/users/me/credits retains balance/reservation/spend fields and adds
monthly_funding with enabled, target_microcredits, defaults, policy, classification,
last_receipt and next_refill_at. The administrative funding read returns the same
funding status for the selected owner. A due unswept period reports the current
database time; otherwise the next refill is the first instant of the next UTC month.

## Upgrade and rollback

Migration 00009 adds classification, owner policies and immutable period receipts.
No existing ledger or wallet amount is rewritten. Existing installations remain
disabled until configured. Expand the schema before enabling the new feature.
Old executors use the same wallet locks and existing reserve/settlement rules.
Older settings writers cannot preserve fields they do not know: disable funding
before rolling back binaries and retain the new tables and receipts. Roll forward
using retained receipts; do not delete them or reset periods to replay a grant.
A schema down migration is allowed only while no funding receipts exist.
