# Artifact capability filtering — 2026-10-12

Previously artifacts.read was enforced at invocation, but users without it still received artifact helper descriptions and the JS namespace. Current rights now control both the model catalog and the exposed sandbox module. artifact_attach remains conditional; every actual artifact invocation still refreshes authorization. Reserved module collision checks and history indexing remain unchanged.

Behavioral tests exercise absent → granted → removed permission and check the model catalog plus actual Goja namespace, without asserting private binding structure. Existing semantic/diagnostics catalog tests and the affected Windows runtime suite passed. Linux runtime race, Golden (zero findings) and Docker build passed. Two fresh independent textual-diff review passes returned no actionable findings, including the final formatted code. The index remained empty during review. Final manual acceptance passed; no production change.

[Manual results](manual-results.json): actual API/River/supervisor execution with a synthetic chat-only role confirmed artifact descriptions and namespace absent; the normal read-authorized role confirmed both present. Each two-call fixture run completed and settled 220 microcredits with zero reserve. No paid OpenAI requests. Image identity is in the receipt. Manual live role revocation was not repeated in this slice; grant/removal visibility is covered by the behavioral test, and prior real-Zitadel revocation acceptance remains separately documented.

Private replay and synthetic role configuration: ignored management.local/loom-night-20261012/permission-qa.ps1 and config.yaml. No production issuer configuration or system certificate trust was changed.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
