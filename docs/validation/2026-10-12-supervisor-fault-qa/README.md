# Supervisor fault acceptance

Local synthetic providers only. API/sandbox image: `sha256:8760177546942b0b4948004b82dd510769c269c9acaf1718ebd368154b086641` (3106e11). No paid API or production changes.

- SIGKILL during a paid script: one tool failed; fixture continuation completed the run with 220 microcredits spent and zero reserve. Readiness returned 503 while the supervisor was stopped. Starting supervisor and restarting API/River did not repeat the script or model calls: two provider calls and one tool step before/after. A fresh healthy script completed. [Receipt](kill-restart-results.json).
- Three simultaneous heap attackers plus one active healthy worker, across two user identities: shared limits 128 MiB, one CPU, 128 PIDs, four workers. Three real OOM events and oom_kill +3; every attacker returned tool_failed while the healthy worker continued and completed with ok=true. Supervisor stayed Running, same container, no restart. All four runs spent 220 each with zero reserve. [Receipt](multi-oom-results.json).

This is bounded fixture acceptance. Shared cgroup isolation does not guarantee a healthy neighbor or supervisor survives every memory workload. Per-worker containers, host-wide exhaustion and target load/admission acceptance remain open.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
