# Sandbox resource acceptance and PID fix — 2026-10-12

Local actual API/River/supervisor/worker acceptance. Models and identity are HTTPS fixtures, not production. Image: sha256:8760177546942b0b4948004b82dd510769c269c9acaf1718ebd368154b086641. Original failures remain separate files; code/compose changes were reviewed before staging.

## Proven failure and repair

The first CPU/neighbor probe on the old fixture had 32 PIDs and no CPU quota. Its healthy script failed, while the supervisor remained alive: [original receipt](initial-results.json), [counters](initial-cgroup-counters.json). Raising only the fixture PID ceiling to 128 removed the healthy failure: [intermediate receipt](configured-results.json). This initial difference alone did not prove the production profile.

A fresh supervisor was then tested with the exact deployment resource limits: four workers, one CPU, 128 MiB shared memory, 32 PIDs. Four scripts across two authorized users reproduced one tool_failed and pids.events max rising from 0 to 20: [corrected-user failure](production-limits-corrected-users.json). Two earlier four-run attempts used a foreign token for the model service's dynamic listing and paused two runs with module_discovery_unavailable, without charges. They did not reach four workers; [first attempt](production-limits-before.json) and [second attempt](production-limits-stable.json) preserve those fixture failures. Paused synthetic runs were canceled through the public API before continuing.

Both current compose profiles now permit 128 PIDs for the same four-worker pool. Linux PID cgroup limits also count Go runtime threads; 32 was insufficient for the measured concurrent workload. CPU, memory, isolation and cleanup limits are unchanged. Four scripts then all returned ok=true, completed with 220 fixture microcredits each and zero reserve; pids.events remained max=0: [after receipt](production-limits-after.json).

## Actual timeout, OOM and recovery

The strengthened fault replay keeps a four-second healthy script active beside the attacker. [Receipts](active-neighbor-fault-results.json):

- Infinite CPU script stopped under the configured ten-second script deadline; the healthy neighbor completed successfully. End-to-end receipt elapsed time was 10.750 seconds, including fixture inference and HTTP polling. This is script timeout acceptance, not SIGINT timing.
- Heap allocation triggered a real Docker oom event and memory.events oom_kill increment. When the attacker had already finished with tool_failed, the neighbor was still running with its first model step settled at 110 microcredits; it then returned ok=true and completed. Supervisor ID and restart count stayed unchanged and Running=true. Docker OOMKilled=true records the cgroup's OOM event even while this supervisor container remains running; it is not evidence of a container restart.
- All four fault/neighbor runs settled 220 fixture microcredits each and released their reserves. A fresh healthy script after both faults also passed: [recovery](active-neighbor-recovery-result.json).

The earlier short-neighbor replay is [separate](final-fault-results.json); its neighbor finished before OOM, so it was not used to prove survival of an active neighbor. Its [recovery receipt](final-recovery-result.json) is retained as well.

Unexpected unsuccessful worker exits now emit warn event sandbox.worker_exited with PID, native exit code and native exec error. Worker stderr, script source, tokens and module payloads are not logged. Success and context cancellation are suppressed; forced cleanup retains its existing sandbox.worker_killed event. [Actual OOM warning events](active-neighbor-exit-events.json), [final counters](final-cgroup-counters.txt).

## Gates and limits

Focused sandbox tests, complete Windows integration, complete Linux race/integration, Golden zero findings, Docker build and both compose config validations passed. One fresh independent code review returned no actionable findings. The index stayed empty during code review; a separate fresh documentation/evidence reviewer also returned no actionable findings.

This is a bounded Docker Desktop fixture, not server acceptance. Shared cgroup memory is still not per-worker memory isolation. Multi-attacker OOM selection, sustained fairness, host OOM, larger worker counts and supervisor-kill recovery remain separate gates. No production container, old accepted QA stand, system certificate or paid model API was changed or used.

Private replay/config: ignored management.local/loom-night-20261012/{parallel-sandbox-qa.py,sandbox-fault-qa.py,resource.compose.yaml}. Fixture callers use two permitted identities; the dynamic model service credential failure was corrected before the four-worker proof.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
