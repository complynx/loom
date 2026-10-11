# Browser acceptance follow-up — 2026-10-12

Result: PASS for the measured local fixture browser flow, on the same reviewed API image `sha256:cef24f773800be422bcbeed05deb4456afe95edc865a416affbf48947f8ad1c4`.

Daniel installed the existing isolated QA certificate into his CurrentUser Root store and restarted Codex. Before restart, Windows HTTPS verification succeeded while the running embedded browser still rejected the certificate. After restart, Docker Desktop was stopped; the agent started the installed Docker Desktop and restored only the retained general QA Compose project using both compose.yaml and rotation.compose.yaml. Existing PostgreSQL, artifacts and session keys were preserved.

The Codex in-app browser then passed these visible checks:

1. Sign in opened the HTTPS fixture issuer without a certificate warning. No TLS override or browser warning bypass was used.
2. The test-only password login returned to `/admin`; the PKCE browser flow authorized diagnostics. The fixture identity is not real Zitadel.
3. Overview rendered available dependencies and financial/recovery counters. Active showed one paused run; All showed 34 runs (28 completed, five cancelled, one paused).
4. Selecting run `00357db4-65f4-4d88-8cc6-99e15de0c9ad` loaded safe detail: completed, spent 110, reserved zero, one completed model step, and no writes/attempts. Responses contain metadata, not prompts or credentials.
5. Returning to Active preserved the selected filter; Refresh advanced the displayed update time. The automatic 15-second refresh was also observed advancing the displayed timestamp with All retained.
6. Disconnect cleared overview, runs and detail and disabled Refresh/Next page.

Proof: [authenticated panel](authenticated.jpg), [disconnected panel](disconnected.jpg). No model run was started by these browser checks and no paid model call was made. Product code and production were not changed.

The earlier [October 11 blocked browser result](../2026-10-11-full-manual-qa/README.md) remains historical evidence; this follow-up closes its certificate/browser-login gate for the tested fixture. It does not replace real production Zitadel acceptance, browser role revocation, expiry or multi-page pagination. Only 34 runs were present, so Next page was disabled; the earlier separate real-Zitadel 50+1 pagination evidence is unchanged.

The panel is left disconnected at http://127.0.0.1:58680/admin. Daniel's explicitly installed test CA trust remains until he removes it. Remove only this certificate after testing with:

```powershell
certutil -user -delstore Root 9863e05ced87904a745defebef2a906435f36a78
```

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
