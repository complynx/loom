# Scoped artifact regression evidence

These are sanitized excerpts copied from command tool outputs in this review session. They are not saved original full logs. No real provider calls were made.

## Atomic module import and UUID boundaries

The added real-PostgreSQL behavior checks cover chat deletion between download and commit, successful import and chat reference, cancellation, quota reuse, and lost commit responses with available or unavailable metadata lookup.

During the first focused run, the atomic-import tests exposed an additional pre-existing boundary fault: PostgreSQL returns canonical UUID text, while artifact files and handles use compact UUIDs. Selected exact output excerpts before the normalization correction:

```text
=== NAME  TestModuleImportPreservesCommittedBlobAfterLostCommitResponse/resolved
    chat_artifact_import_test.go:140:
        Error:      Received unexpected error:
                    commit response lost
=== NAME  TestModuleImportIsReadableAndCollectedWithChat
    chat_artifact_import_test.go:96:
        Error:      Not equal:
                    expected: "663b7dc6c3eca0625c5a8cb21843fa5f"
                    actual  : "663b7dc6-c3ec-a062-5c5a-8cb21843fa5f"
FAIL
FAIL github.com/complynx/clx-scripter/internal/store 3.315s
```

This initial test invocation also had a recovery fixture flaw: it opened a different temporary directory. That fixture was corrected to reopen the same artifact directory. Its failure is not counted as evidence of a product defect. The commit-response failure and differing UUID forms above are independent of that fixture flaw.

After the product and fixture corrections:

```powershell
$env:CLX_TEST_DATABASE_DSN='postgres://clx:isolated-review-tests-only@127.0.0.1:55439/clx_test?sslmode=disable'
go test -count=1 ./internal/store -run 'TestModuleImport' -v
```

Selected exact result excerpt:

```text
--- PASS: TestModuleImportRejectsDeletedChatWithoutConsumingQuota (0.43s)
--- PASS: TestModuleImportPreservesCommittedBlobAfterLostCommitResponse (0.00s)
    --- PASS: TestModuleImportPreservesCommittedBlobAfterLostCommitResponse/unresolved (0.44s)
    --- PASS: TestModuleImportPreservesCommittedBlobAfterLostCommitResponse/resolved (0.44s)
--- PASS: TestModuleImportIsReadableAndCollectedWithChat (0.45s)
PASS
ok github.com/complynx/clx-scripter/internal/store 1.897s
```

Further checks added strict compact/canonical UUID validation, metadata-handle reuse, physical collection and quota reuse, and cancellation. The complete focused packages then passed against real PostgreSQL:

```powershell
go test -count=1 ./internal/artifacts ./internal/store ./internal/app
```

```text
ok github.com/complynx/clx-scripter/internal/artifacts 1.226s
ok github.com/complynx/clx-scripter/internal/store 18.837s
ok github.com/complynx/clx-scripter/internal/app 4.551s
```

The original two-step import implementation was confirmed by source inspection. It was not rerun after adding the atomic-import regression; do not describe that finding as a saved before/after test failure.

## Concurrent attachment and chat deletion

The regression uses public CreateRun and DeleteChat. The queue hook holds CreateRun after its artifact attachment; pg_blocking_pids confirms that deletion is waiting on the attachment transaction. After that attachment commits, deletion must preserve the shared blob. Deleting the surviving chat must subsequently tombstone it.

The test was run against the original deletion statement before applying the separate candidate-lock statement:

```powershell
go test -count=1 ./internal/store -run 'TestDeleteChatPreservesArtifactAttachedWhileDeletionWaits' -v
```

Selected exact output excerpt:

```text
=== RUN   TestDeleteChatPreservesArtifactAttachedWhileDeletionWaits
=== PAUSE TestDeleteChatPreservesArtifactAttachedWhileDeletionWaits
=== CONT  TestDeleteChatPreservesArtifactAttachedWhileDeletionWaits
    chat_artifact_delete_race_test.go:82:
        Error:      Received unexpected error:
                    artifact not found
--- FAIL: TestDeleteChatPreservesArtifactAttachedWhileDeletionWaits (0.37s)
FAIL
FAIL github.com/complynx/clx-scripter/internal/store 1.813s
```

After the deletion fix, 10 real-PostgreSQL Windows repetitions passed:

```powershell
go test -count=10 ./internal/store -run 'TestDeleteChatPreservesArtifactAttachedWhileDeletionWaits'
```

```text
ok github.com/complynx/clx-scripter/internal/store 5.250s
```

Ten Linux repetitions with the race detector also passed, using the pinned Go image, repository and existing Go caches mounted into Docker, and the same isolated PostgreSQL through host.docker.internal:55439:

```sh
apk add --no-cache gcc musl-dev >/dev/null && go test -count=10 -race ./internal/store -run TestDeleteChatPreservesArtifactAttachedWhileDeletionWaits
```

Image: `golang:1.27.2-alpine3.24@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d`.

```text
ok github.com/complynx/clx-scripter/internal/store 7.721s
```

All corrected commands above returned exit code 0. Both before-correction test commands returned exit code 1. Source line numbers and timings belong to the invocation shown and may change after later edits.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
