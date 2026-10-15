# Isolated restore and recovery acceptance

Only synthetic identity/model peers and the model acceptance service were used. No paid OpenAI calls, production mutation, trusted-root installation or changes to accepted source storage. A separate PostgreSQL database and artifact volume were restored with the source session keyring.

The snapshot captured an actual Running run, pending model step, 9,997 microcredit reserve and River job. Source inference then completed and settled 110 credits. A separate confirmed model-service write completed after the snapshot. The restored database lacked that write's run; the external service retained its durable state.

## Verified

- Recovery health/diagnostics available; readiness and execution/default-chat/SSE/settings/artifact mutation routes return 503 recovery_mode. No extra provider calls or external effects. Four blob files match SHA-256 inventory. Running cancellation preserves the hold and records cancel_requested. [Initial barrier receipt](barrier-results.json).
- A fresh trusted service absent from restored service_identities cannot use its broad token as a human. Bare token 401; delegated default-chat reaches recovery barrier; delegated diagnostics/settings 403. After successful manifest removal both bare/delegated tokens return 401. No identity row created in the restored database. [Security receipt](service-classification-results.json).
- Existing Store.ClaimRun uncertainty guard, called by a QA-only developer one-shot after lease expiry, moved the pending run to reconciling without a provider. This transition is **not a shipped operator endpoint**. Owner cancellation then terminalized it. Evidenced receipt reconciliation rejected an unprivileged caller, accepted the same admin receipt twice, added one ledger entry, settled 110 credits and released reserve. No provider call during repair or recovery exit. Inventory showed no queued/running runs before restart with recovery disabled. A fresh request then completed with 110 spent and zero reserve. [Repair/exit receipt](repair-exit-results.json).
- Missing active session key and empty restored database refuse startup. [Startup failure receipt](startup-final-results.json). Final-image quarantine readiness/default-chat checks and persistent recovery metric pass: [final barrier receipt](final-barrier-results.json).

Initial barrier image differs from final security/repair image; receipts record each digest. Final image: sha256:b8ee206cfa227fc1af93492832bf6284ed54b22d421bacf26fdf5c145a071fc2. The disposable restored stand is left in recovery mode. Whole Windows integration, Linux race/integration, Golden zero findings and image build passed after the fixes. Three fresh code review passes: first found discovery-removal downgrade, second caught a compile defect, third no actionable findings. Root acceptance also found and fixed delegated diagnostics rights. The index stayed empty during reviews.

## Limits and retained failures

This is an explicit pg_dump/blob/key recovery set, not PITR/WAL, RPO/RTO, off-host backup delivery or target-server acceptance. Source blob writes were quiet during the blob copy; this test does not implement an atomic online backup protocol. Missing post-snapshot effects/receipts are not reconstructed automatically. Running cancel is request-only while workers are off; an operator preparation command/API for expired uncertain Running runs is still needed for a complete recovery workflow. Existing reconciliation only accepts provider model/summary/compact steps, not unknown script/tool writes.

Recovery remembers newly observed service identities only for the process lifetime. Before a recovery restart, retain all service subjects since the backup in deployment identity.service_subjects; a snapshot cannot identify an absent, previously unknown service on its own. Complete identity inventory is an operator prerequisite.

QA-driver failures are preserved separately: wrong DiagnosticStep field, missing cat in scratch image, invalid manifest diagnostics permission and empty fixture request counter. They were corrected on the same restored snapshot. No failure was relabeled PASS.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
