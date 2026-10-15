# Artifact Range acceptance — 2026-10-12

Implemented standard-library HTTP byte ranges and conditional delivery for direct artifact and temporary-handle content. HEAD returns full headers only and ignores Range/If-Range. Owner/role/handle expiry checks precede all conditional results; existing authorization-refresh and transfer cancellation/deadline behavior remains active.

Focused public HTTP/PostgreSQL acceptance passed full/middle/suffix/open-end reads, matching/stale If-Range, ETag 304, failed If-Match 412, invalid/outside 416, foreign-owner denial and expired-handle denial. Separate tests passed multipart byte ranges and context-canceled seeking. Public OpenAPI declares binary/any-MIME responses, headers, status codes and HEAD.

Manual candidate acceptance: [results](manual-results.json). Actual isolated API/River deployment served a 1 MiB artifact, a 16-byte middle range, HEAD/ETag and denial cases; a completed fixture model run produced a temporary handle supporting a suffix range. One local fake inference settled 110 microcredits with zero reserve. No paid OpenAI request. Candidate image at this measurement: sha256:e5e551a14ca11d7bf82fa1e1d775fc9be1a3a2d6fe6370373143376433a792c7. Candidate results remain separately in [candidate results](manual-candidate-results.json). Final manual replay passed on image sha256:a95605cd20e11d395b3052304db31a89568bbc9766c7204d1340136ec6d5da3c, including HEAD with partial and unsatisfiable ranges. Its fixture run receipt is in manual-results.json.

The isolated project is loom-night-qa-20261012, API loopback58780. Private synthetic fixture/replay data are in ignored management.local/loom-night-20261012; prior QA stands and production were unchanged. Test CA is mounted only for the fixture clients; no Windows root certificate was installed.

Golden 0 findings after required helper/test simplification; no rule suppressions were added. Two independent first-pass reviewers identified HEAD Range semantics; that defect was fixed. A fresh second-pass reviewer returned no actionable findings. The promoted-field literal concern was rejected against the pinned Go 1.27.2 compiler and Golden modernize output. Index stayed empty throughout review. Final Windows integration, Linux race/integration, Golden and Docker build passed. Native image model-input handling and full ordinary downloads retain their existing behavior.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
