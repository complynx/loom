# Sandbox health capacity handshake — 2026-10-11

Status: deterministic regression reproduced, fixed, and locally checked on Linux. No production rollout.

The busy supervisor sent a capacity response and closed the socket before reading the client's request. A health caller could therefore receive a broken pipe when sending `ping`. The historical intermittent failure remains recorded in the original QA log.

The new behavioral test holds one connection to occupy the only slot, connects a second client, and verifies the supervisor waits for a request before replying. It then sends `ping` and receives `pong` without starting a worker. The old implementation failed this test. The saturated path now reads the request under a 100 ms connection deadline before sending `pong` or a capacity error. Shutdown cancels the connection. The accept loop handles this bounded overload handshake synchronously, so an idle overload connection can delay later accepts by at most 100 ms; it does not create unbounded rejection goroutines.

[Before fix](before.txt): deterministic regression FAIL. [After fix](after-linux-race.txt): the complete sandbox package passed ten times on Linux with the race detector, including worker execution, cancellation, health, and capacity handshake. This proves the identified protocol defect is fixed, not every possible future IPC failure.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
