# On-demand method help acceptance

Local container acceptance PASS, 2026-10-12. Scope: permission-filtered `modules.help.list` and `modules.help.read`, builtin cards and current service method contracts. No production or real provider calls.

Two real API/sandbox runs verified six assertions: literal search and list continuation; bounded card continuation; reading a PDF using the documented parameters; service input/output schemas; paid-action classification; hiding file help without artifact rights. Both completed with zero reserved microcredits. [Results](results.json) contain selected assertions and run IDs only.

PostgreSQL behavior tests also verify live permission removal, disabled semantic-search help, invalid offsets and UTF-8 boundaries. Golden lint passed. Full Linux integration with race detector passed. Full Windows integration passed after the effect-classification fix.

Independent review loop: two passes. Corrected first-pass finding: paid/action model functions must not be labeled read-only. Second pass found no actionable findings. Staging stayed empty through review. Review assumes registry authorization and file behavior; actual PDF and permission behavior were additionally accepted through the running stand.

Builtin docs are compiled from `internal/app/help_cards.json`; repository Markdown is not exposed as filesystem access. External service schemas/descriptions are read from the registered current contract. Lists return at most 50 entries with short summaries. Cards return at most 32768 text bytes with UTF-8-safe continuation. Dynamic service discovery still uses its configured cache policy. Service-authored descriptions are untrusted data; examples and schemas cannot grant permissions.

Container image: loom:file-help-20261012, sha256:f0e571b970c5a7d26de38f60b4d38b4f96df0d875c63bcd464ca2dc8918bd8f9.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk