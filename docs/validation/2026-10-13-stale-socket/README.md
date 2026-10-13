# Stale-socket fixture race

The first push CI attempt for 41daf54 failed in TestWorkerReclaimsStaleSocket:
`worker stopped before readiness: file worker socket is active`.
The parallel PR CI and the retry passed. The investigation below reproduced the
failure; it is not a readiness timeout.

## Cause

The test called listen, kept the socket pathname, then closed the listener.
Parallel fileproc media tests start external processes. Linux process creation
copies descriptors; CLOEXEC closes them at exec, not at fork/clone. A child can
therefore hold a copy of the listening descriptor briefly after the parent closes
its own copy. The worker's connection probe then succeeds and correctly rejects
the active socket.

References: [Go Linux process creation](https://go.dev/src/syscall/exec_linux.go),
[Linux fork](https://man7.org/linux/man-pages/man2/fork.2.html),
[Linux execve](https://man7.org/linux/man-pages/man2/execve.2.html).

## Evidence

All experiments used the pinned Go 1.27.2 Alpine Linux container. Media tests used
FFmpeg 8.1.2-r0 and Poppler 25.12.0-r1.

- Original socket tests alone: 5000 repetitions passed.
- Original full fileproc media suite with race detector, parallel=4: 2 matching
  failures in 150 repetitions.
- A 30-second standalone listen/close/connect probe with concurrent process
  creation: 65,927 successful connections after close in 353,336 attempts.
- Control without process creation: 0 in 954,400 attempts.
- Bind-without-listen probe with concurrent process creation: 0 in 578,129 attempts.

The standalone counts characterize the experiment, not the CI failure rate.
Ignored diagnostic source is in management.local/socket-flake-study/probe.go.txt.

## Fix and verification

Create the stale socket fixture with socket -> bind -> close, without listen.
A briefly inherited descriptor cannot accept a connection because it never
listened. Close the descriptor even when bind fails. The resulting socket file
still exercises stale-path removal and worker startup. Active-socket and unsafe
path rejection tests remain unchanged. Runtime code is unchanged.

After the fix, all 150 repetitions of the same full fileproc media/race suite
passed. The full Linux race/integration suite also passed (unaffected packages
used the Go test cache). Golden lint with GOOS=linux and integration tags reported
0 issues. Independent diff review: one pass, no actionable findings; staging
remained empty until review completed.

Reproduce the media gate inside the pinned Linux environment:

```sh
CGO_ENABLED=1 go test -race ./internal/fileproc -count=150 -parallel=4 -timeout=180s
```

No production services, permissions or paid providers were changed.

Written by Codex (gpt-6/Codex desktop), developer of Loom
on behalf of Daniel Drizhuk
