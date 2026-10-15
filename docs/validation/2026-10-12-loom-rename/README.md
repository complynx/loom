# Repository rename — 2026-10-12

The maintained project now uses github.com/complynx/loom, cmd/loom, the loom executable, complynx/loom images and updated-loom deployment dispatch. Git origin fetch/push URL is git@github.com:complynx/loom.git. Go imports, build scripts, OpenAPI/operator titles, manifest schema IDs, documentation, Compose, alert names/selectors and prepared monitoring configuration were updated together.

Historical QA logs, source manifests, snapshots and ignored existing stands keep the identity of the tested release. Existing CLX_* configuration, clx_* metrics and clx.harness.* protocol labels remain compatible. Git object history and reflogs were not rewritten; the active repository URL in .git/config changed.

Checks: Go tests with integration tags passed (database scenarios skip without CLX_TEST_DATABASE_DSN); Golden passed with zero issues; operator UI behavior test passed; Linux Docker image build passed; promtool accepted all 10 rules and alert fixtures passed. The 171 dependency versions match the unchanged commit lock and Docker verified go.sum. Full canonical-origin lock re-resolution was stopped because it was slow; no new dependency provenance is claimed.

Alert fixture initially expected an obsolete unknown-inference annotation. It was aligned with the existing bounded platform-paid recovery description, then passed.

The local sibling server receiver and Alloy mapping were updated to updated-loom, service loom and integrations/loom. The coordinated patch is retained in the operator-local configuration pack; it is not committed/pushed in server_configs or deployed. Publish its receiver before main CI dispatches the renamed event. Docker Hub credentials must authorize complynx/loom.

No main push, image publication, production change or data migration occurred.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
