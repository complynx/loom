# Prepared general manual QA matrix

Status: PREPARED. Final acceptance has not started. Product image must be rebuilt after the review loop.

The lane uses public HTTP clients against the complete Linux API, River workers, separate sandbox supervisor, PostgreSQL with pgvector, Docker metadata discovery, and the acceptance service. Identity and Responses endpoints are explicit test peers. No real model call is authorized in this lane.

| Feature | Planned observable acceptance | Boundary |
| --- | --- | --- |
| Direct identity | invalid bearer rejected; owner/org isolation; no-rights caller has personal memory | HTTPS introspection fixture; actual Zitadel in separate lane |
| Service delegation | trusted caller manifest; subject token bootstrap; bare service denied; owner binding and revoke | fixture credentials, real runtime classifier |
| Chats and admission | stable default; explicit chats; duplicate run replay and conflict; one active run per chat | actual PostgreSQL admission and River |
| Passive events and history | deduplication; no run/inference; literal/fulltext/regex; invalid regex; bounded partial reads and cursor | client-driven HTTP plus provider-input inspection |
| Semantic vectors | index nonzero vectors; nearest match; owner isolation; disable config removes route/tool | supplied vectors; no embeddings service |
| Run interaction | choices/text/none; answer conflict; steering; pause/resume; cancellation; SSE replay | deterministic programmable provider, actual scheduler |
| Model policy and credits | preferences CAS; admin policy/settings; denied admin; audited adjustment; ledger spend/reserve | test tariff; no invoice claim |
| Diagnostics | permission denial; bounded pagination/detail; script functions; private metrics | browser operator login handled by root/issuer lane |
| Personal memory | micro 500 limit and prompt inclusion; index/note/source CRUD; CAS; clear; bounded reads; isolation; script calls | no auto extraction or model quality claim |
| Artifacts | upload; authenticated stream; foreign owner denial; temporary handle; JS uses handle metadata | supported MIME/model capability gate inspected |
| Discovery and modules | actual Docker labels; static/dynamic catalogs; permissions filter; read result blob | stale endpoint/manifest transitions require explicit fault scenario |
| Effectful modules | approval; reject; duplicate approval; lost reply; retained-volume restart; status recovery | fixed fixture business effect |
| Provider recovery | zero-output 429/503 short and durable retries; unknown transport and accounting | deterministic Responses fixture, no paid retry |
| Context and cache | previous_run_id; passive compression; bounded active compaction if supported | no claim about real OpenAI cache savings or summary quality |
| Session encryption and retention | operator rotation and deletion/retention checks where available | backing up server data and production rollout remain separate |
| ENOSPC / queue restart reauthorize | covered by assigned independent fault lanes | not duplicated here |
| Local Codex adapter | current subprocess tests; live use only with separate dedicated login | unavailable live acceptance; no credential copying |

Stand: API `http://127.0.0.1:58680`, metrics `http://127.0.0.1:58681`, acceptance fixture `http://127.0.0.1:58690`, programmable peer control `http://127.0.0.1:58691`. Project `clx-fullqa-general-20261011`; network `10.254.244.0/24`. Only owned containers/volumes are changed. Private keys stay in ignored `management.local/full-qa-general/`.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
