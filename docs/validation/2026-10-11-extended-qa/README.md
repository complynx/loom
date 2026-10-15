# Расширенная локальная QA — 11 октября 2026 года

Рабочее дерево после базового MVP `25e3850`; изменения не закоммичены. Финальные полные Windows integration, Linux/Docker race и Golden 0 findings gates после всех fixes PASS, exit 0. Retry прошёл шесть свежих review passes; последний без actionable findings, предыдущие findings исправлены. Исходные fault FAIL сохранены; cancellation, shutdown и health capacity handshake позже исправлены и приняты в [repair QA](../2026-10-11-fault-fixes/README.md). Disk-full unverified. Память имеет два чистых review и local acceptance; ровно один live model call PASS. Production-приёмка не выполнена.

| Область | Подтверждённый результат | Evidence и граница |
| --- | --- | --- |
| Общий Windows gate | Исходный и новый PASS | [windows-tests.txt](windows-tests.txt), [final-windows-tests.txt](final-windows-tests.txt). Последний gate включает memory, scope и dispatch-guard правки. |
| Общий Linux/PostgreSQL `-race` | Финальный PASS; ранние failures сохранены | [final-linux-race.txt](final-linux-race.txt): полный gate после всех fixes PASS, exit 0. Исходный [linux-race.txt](linux-race.txt) cancellation FAIL сохранён отдельно. |
| Supervisor health IPC | Reproduced and repaired locally | Original [broken-pipe FAIL](linux-race-health-failure.txt) retained. New capacity handshake regression failed before the fix, then passed; [repair QA](../2026-10-11-sandbox-health-fix/README.md). |
| Dependency notices/vulnerabilities | PASS | [362 notices без mismatches](notices.txt); [govulncheck: No vulnerabilities found](govulncheck.txt). [Полный audit](../../dependency-audit/2026-10-11-mvp/README.md) ограничивает область приложения и отделяет devtool graph. |
| Golden / Promtool | PASS проверенного снимка | [final-golden.txt](final-golden.txt): 0 findings. [alerts-final.txt](alerts-final.txt): 10 правил, exit 0. [golden.txt](golden.txt) сохраняет исходные замечания. Общий gate после последних memory/scope/retry guard правок также PASS. |
| Настоящий локальный Zitadel | 18 дополнительных PASS | [Расширенный report](../2026-10-11-zitadel-extended-qa/README.md): cross-org отказ, live role removal, expiry/fresh credential, настоящая signing-key rotation и issuer outage. Identity/session/HTTP; полный queue restart → reauthorize и installed issuer gate не проверены. Это дополнение к прежним 27 PASS. |
| Полный локальный Linux runtime | PASS ограниченной функциональной области | [REPORT](../2026-10-11-full-runtime-qa/REPORT.md): River, настоящий sidecar supervisor/worker, Docker proxy/metadata discovery, JS dynamic lookup, blob import/handle/download, approval/lost reply/restart/status recovery, question, steering и обычный cancel. Issuer/provider — детерминированные HTTPS peers. Снимок image предшествует новым diagnostics/UI изменениям. |
| PG outage/restart | Recovery PASS; shutdown repaired locally | Original 5854.65 ms Docker signal-to-die event FAIL retained in [runtime report](../2026-10-11-full-runtime-qa/REPORT.md). New process-exit and notification timings are separated in [shutdown repair](../2026-10-11-shutdown-fix/README.md). |
| Worker kill recovery | PASS на Windows и Linux race | [Failure QA](../2026-10-11-failure-qa/report.md): фактическое завершение engine subprocess после inference и после committed service-write до checkpoint, естественное lease expiry, отсутствие повторного tool effect. Provider/service callback — test doubles, эффект в настоящей PG. Это не kill всего deployment. |
| Cancellation checkpoint | Reproduced and repaired locally | Original [FAIL](../2026-10-11-failure-qa/report.md) retained. Interrupted state read checkpoints durable control; exact post-inference scenario now cancelled/client_cancel, spent 15/reserved 0. [Repair QA](../2026-10-11-cancel-fix/README.md). |
| Disk-full/ENOSPC | Не проверен | Ни artifact write, ни durable state boundary под заполненным диском не получили acceptance PASS. |
| Diagnostics/API, agent functions, hybrid retry | Local PASS / production pending | Diagnostics JS и browser connect/filter/detail/disconnect PASS; pagination50+1/refresh приняты реальным browser. Все retry findings исправлены, шестой свежий review без actionable findings; последние общие gates PASS. Atomic claim/run-lock/River snooze, pause/cancel Retry-After, early resume409, saturated waits и штатный inference timeout проверены. Общий circuit breaker/другие dependencies остаются за локально принятой provider retry областью. |
| Прямой Zitadel panel login | Real local browser PASS; stand retained / target pending | [QA report](../2026-10-11-panel-login-qa/README.md): настоящий issuer, PKCE/resourceowner scope, 51 metadata run, pagination 50+1, та же вторая страница после 30 секунд refresh, detail. Live grant removal дал old-token auto 403 с очисткой данных; fresh SSO login без роли тоже 403. Restore grant, fresh SSO login с 50 строками и Disconnect с очисткой данных PASS. Stand сохранён на [localhost:58380/admin](http://localhost:58380/admin), model calls = 0. Четыре свежих auth review чистые. Production client отсутствует, rollout pending. |
| Персональная память | Local PASS; no separate roles | Every authenticated user accesses their own issuer/subject/org-scoped memory. Index → notes → sources plus micro 500 Unicode characters reloaded each inference. Default-access tests, fresh source review and final broad gates PASS; [Memory QA](../2026-10-11-user-memory/README.md). Production pending. |
| Живая модель | Один connectivity/accounting PASS | [LIVE-RESULT](../2026-10-11-openai-live-qa/LIVE-RESULT.md): ровно один gpt-6-luna/medium model call, LIVE_OK, 664 input / 6 output, 70 microcredits, 0 reserve. Initial disclosure_denied дал 0 dispatch; после stand-only correction — один explicit resume, без inference retry. Tools/images/continuation/quality gate остаётся pending. |
| Production | Pending | Installed issuer/client, deployment, backup/restore и rollout не приняты. |

Healthy SIGTERM в полном runtime: API 296.02 мс, supervisor 47.82 мс по Docker signal-to-exit events. Исходный outage FAIL сохранён; новая scoped repair acceptance приведена выше. Сервисный write восстановился с одним сохранённым effect; полный runtime lane не измерял точное количество HTTP RPC попыток.

Критерии завершения остаются в [development-track](../../development-track.md), проверенные границы — в [mvp-limitations](../../mvp-limitations.md). Новые локальные изменения прошли финальные общие gates и отдельные review. Эти PASS не закрывают сохранённые faults или production acceptance.

Этот документ обобщает сохранённые результаты. Проверки использовали локальные disposable stands; production server и production credentials не применялись. Ровно один разрешённый платный model call выполнен в отдельном live stand; остальные lanes использовали тестовые provider peers. Docker cleanup logs относятся к соответствующим lanes, а не к удалению чужих ресурсов.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk








