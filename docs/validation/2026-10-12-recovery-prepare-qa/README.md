# Shipped recovery preparation acceptance

A new disposable database prepare_recovery_20261012 was restored from the original isolated snapshot. A separate API/container and copied artifact volume used synthetic identity/model peers. Accepted source databases, old reports and production remained untouched. Image: sha256:a10b35dc07ef0658fdd2b61da9920008cbfcc1b4b88c8ceb328fa36089341c23.

## Manual HTTP acceptance

[Receipt](results.json): restored actual pending model step, reserve 9,997, fence 1. Four concurrent identical preparation requests through the shipped recovery API all succeeded; one audit record, fence 2, unchanged pending step/reserve/spent. No developer ClaimRun helper was used. A deliberately live fixture lease returned 409; stale fence 409; missing right 403; changed evidence under the same key conflicted. These clock changes were fixture setup, not an operator procedure.

Owner cancellation after preparation and two identical evidenced settlement requests used shipped endpoints. Final state cancelled, spent 110, reserve zero, one settlement entry. Provider requests added zero; the external acceptance-service state stayed byte-for-byte unchanged. After deliberate inventory-safe exit, preparation returned 409 recovery_mode_required. Stand was returned to recovery.

## Developer checks

Behavioral PostgreSQL tests cover preparation, scope/right/evidence/lease/fence/state guards, preserved cancel request, no scheduling, one audit, unchanged holds, duplicate settlement and unknown external write preservation. Windows integration, Linux race/integration, Golden and image build are the release checks for this stage. The platform-only exposure guard is implemented but has no separate manual fixture acceptance.

Two independent source reviews: quality/contract reviewer found no actionable findings; correctness reviewer proposed replacing Go promoted embedded-field literals, rejected as a false positive for pinned Go 1.27.2. Compiler, image build and Golden modernize accept/require the current form. No substantive code defect remained. Index stayed empty during review. Initial test-driver expectations were corrected: a pre-existing reserve ledger entry is not a settlement, and a queued run has fence zero so a submitted positive fence conflicts first.

This closes the historical need for a developer-only preparation helper. It does not supply PITR/off-host backups, reconstruct missing post-snapshot operations, settle unknown writes/platform exposure, or enforce stopping other replicas. Production recovery remains unaccepted. Historical restore reports retain the previous limitation as their snapshot status.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
