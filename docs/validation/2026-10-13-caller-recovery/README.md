# Caller binding recovery acceptance — 2026-10-13

Issue: https://github.com/complynx/loom/issues/2 (L01 in Shuttle findings). Local acceptance PASS. Source: the reviewed caller-recovery diff after f0090ca, committed with this report. No production, Telegram delivery or paid provider requests.

## Evidence

- Regression reproduced against unchanged source: expected retryable 503, actual opaque 401 after caller removal.
- Full Windows integration PASS; full Linux integration/race PASS; Golden lint PASS.
- Two independent diff review passes. First pass identified that missing binding could mask revoked delegated user/session; corrected auth ordering and added combined absence/revocation cases. Second pass: no actionable findings. Staging remained empty throughout review.
- Actual Zitadel 4.16.3 from the existing isolated IAM, independent Loom/PostgreSQL/caller Docker project `loom-caller-qa-20261013`, real Docker discovery, encrypted sessions and River. The existing Shuttle/Telegram services were not restarted or changed.
- Image: loom:caller-recovery-20261013, sha256:8d1010f11a2f1d8232a4143cb9a28bbff9083e0ca17330a7506a3bda81e5ddef.
- Real IAM status cycle 200 → 503 caller_binding_unavailable, retryable=true → 200. Same run/session retained. The worker reached waiting_auth; explicit same-session reauthorization completed that run. One unique mock inference request ID, zero final reserve, 15 microcredits from explicit scenario usage (not measured model billing).
- Invalid actor/user tokens during caller absence: 401. Removed caller chat permission: 403. Revoked saved session during caller absence: 404, retryable=false.
- Synthetic online-introspection tests additionally cover actual inactive actor/user tokens, user module-role removal, ambiguous bindings, saved-session revocation and execution authorizer recovery. Service harness rights remain separate from delegated user module rights.

## Failures and boundaries

The first real-IAM script assumed an automatic final completion after discovery recovery; the worker correctly retained waiting_auth. The same run completed after explicit reauthorization. Later QA-script corrections handled empty 204 responses and waited for the stable caller-binding error instead of an intermediate dependency_unavailable/discovery transport error. Those attempts remain failures, not additional passes.

Real IAM token revocation or role mutation was not performed against shared test users/actors; those paths use synthetic online-introspection tests. The caller is a minimal manifest fixture, not the actual Shuttle application. A full Shuttle/Telegram repeat acceptance with the new Loom release remains separate. No stale binding grants are retained. A retryable result is not proof that credentials/permissions remain valid or that an intentionally removed service will return.

The stand stays isolated and preserves its QA metadata. Published results exclude JWTs, secrets, raw model requests and private config.

Written by Codex (gpt-6/Codex desktop), developer of Loom
on behalf of Daniel Drizhuk