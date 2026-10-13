# Method deadline test acceptance

The first push CI attempt for e072e29 failed at registry_test.go:683 in
TestMethodDeadlineIsIndependentOfManifestDeadline. The broker returned
context.DeadlineExceeded as expected, but the server cancellation counter never
reached one. The parallel PR CI and push retry passed.

## Test defect and correction

A request with a 30 ms deadline can expire before reaching the server. The old
test required server-side cancellation even in this valid outcome. The CI log has
no delivery trace; it proves the failed assertion, not the precise scheduling
sequence. The assertion was invalid regardless of where that request expired.

Keep the existing behavioral deadline checks: a 250 ms response succeeds with a
1000 ms method deadline despite a 100 ms manifest timeout, and a 30 ms method
deadline returns context.DeadlineExceeded.

Check transport cancellation separately. The new test waits until the real HTTP
handler has consumed the request body, then cancels the caller context. The call
must return context.Canceled and the server must observe request cancellation.
An independent handler-stop channel lets failure cleanup finish even if the
cancellation behavior under test is broken. A five-second test context bounds all
waits. No runtime code or timeouts were changed.

## Verified

- Windows: 50 repetitions of the two focused behavioral cases passed.
- Windows full modules suite passed after the final cleanup change.
- Golden lint for internal/modules: 0 issues.
- Linux: 50 repetitions of the full modules suite with race detection passed.
- Review loop: the first review found unbounded failure cleanup; corrected.
  A fresh second review found no actionable issues; staging stayed empty.

## Docker lifecycle

Following Daniel's full Docker prune, this acceptance used only the disposable
loom-deadline-qa container with --rm. No PostgreSQL, Zitadel or service fixture was
started. The downloaded Go test image was removed after acceptance. Module download cache
stays in ignored management.local/deadline-qa.
No production deployment or paid provider calls were performed.

Written by Codex (gpt-6/Codex desktop), developer of Loom
on behalf of Daniel Drizhuk
