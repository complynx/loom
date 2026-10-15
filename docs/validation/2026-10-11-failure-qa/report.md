# Failure and recovery QA — 2026-10-11

Verification only. No product files were changed. Scope: process loss at durable inference/tool boundaries and the cancellation race reported by the full Linux race gate. PostgreSQL was the isolated shared QA instance at port 55439, with a fresh disposable schema for each case. Providers, user authorization, and service dispatch were test doubles. No real model, production service, or production credential was used.

## Results

| Scenario | Boundary | Result |
| --- | --- | --- |
| Worker killed after inference dispatch | Actual child-process termination on Windows and Linux; pending inference row already committed in real PostgreSQL. The provider double entered Respond and blocked. | PASS. Replacement worker made one authorized inference. One uncertain platform attempt recorded. Final state completed, user spent 15, reserved 0. |
| Worker killed after service write, before tool result checkpoint | Actual child-process termination; Script callback inserted one committed effect row in real PostgreSQL and blocked before returning. | PASS. Replacement refused uncertain work, returned inference_uncertain, and left reconciling. Exactly one effect row remained. No duplicate script dispatch or replacement inference. User spent 15 for the verified model reply; reserved 0. |
| Existing cancel vs late inference test | Unmodified existing test, focused repetitions. | PASS 100/100 on Windows; PASS 20/20 Linux with race detection. The original full Linux race gate FAIL remains valid. |
| Cancel while the execution loop is loading durable state | Deterministic scratch Store wrapper delays only executionLoop's ExecutionRun call after successful inference; API cancel receives 200; Engine.Stop cancels the delayed read. | FAIL. Execute returns context canceled. Durable run remains running with cancel_requested=true, empty result, spent 15, reserved 0. Terminal client_cancel checkpoint is absent. Reproduced on Windows and Linux race. |

The scratch diagnostic test itself exits successfully because it asserts the defect observations. Its harness PASS does **not** mean cancellation behavior passed acceptance.

## Cancellation defect

The original unmodified full-gate failure is recorded in ../2026-10-11-extended-qa/linux-race.txt: TestCancelDuringInferenceCannotBeOverwrittenByLateFinal received context canceled at integration/critical_inference_test.go:127. That assertion ends the test before it checks the final durable run state. Focused repetitions did not reproduce the original scheduling interleaving.

The separate deterministic diagnostic proves one cancellation checkpoint gap at internal/runs/engine.go:763-765. executionLoop checks ctx.Err(), then loads ExecutionRun using the cancellable context. If cancellation occurs during that load, it returns the load error directly. finishInterrupted is bypassed. The cancel request remains durable, and the final answer is not published, but the worker exits with the run still marked running. A subsequent queue retry or lease recovery may settle the request; this test does not claim the request is permanently lost. No cause beyond this proven read window is assigned to the intermittent full-gate failure.

The smallest responsible repair point to consider is the execution boundary's handling of context cancellation from database/turn calls, so that it loads fenced state and records interruption before exit. No fix was applied.

## Evidence and reproduction

- cancel-windows.txt: unmodified focused test, count 100.
- cancel-linux-race.txt: unmodified focused test, count 20, race enabled.
- process-boundaries.txt: actual Windows process kills and deterministic cancellation diagnostic.
- process-boundaries-linux-race.txt: actual Linux SIGKILL and cancellation diagnostic with race detection.
- failure_process_test.go.txt: retained scratch overlay source, copied into this evidence folder. It is not a permanent integration package test.
- overlay-windows.json and overlay-linux.json: retained Go overlay configurations. The scratch source is management.local/failure-qa/failure_process_test.go.txt.

Windows crash/diagnostic command: go test -overlay=management.local/failure-qa/overlay-windows.json ./integration -run '^TestQA(RealWorkerKillBoundaries|CancelAtDatabaseReadDiagnostic)$' -count=1 -timeout=45s -v. Set CLX_TEST_DATABASE_DSN to the isolated QA database first.

Linux uses golang:1.27.2-alpine3.24@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d, the repository mounted at /src, the Linux overlay, and the same command with -race. The host database address is host.docker.internal:55439.

Process-kill tests used a 300 ms test lease and waited 400 ms after killing the child. Lease expiry occurred naturally; no SQL update forged an expired lease. Windows Kill terminates the child process; Linux Kill sends SIGKILL. These are engine worker subprocess tests, not the complete HTTP/River deployment process. They prove durable recovery at the engine boundary, including effects in PostgreSQL. Real HTTP provider dispatch and the actual acceptance booking-service boundary are outside this test's scope.

Each subprocess was waited for after termination. Test schemas and effect rows were removed by fixture cleanup. Linux runners used --rm. The shared PostgreSQL container was not stopped or changed. Existing sponsored-cap/billing, compaction-no-repeat, graceful-stop, and receipt-reconciliation tests are covered by the root's full gates; they were not redundantly repeated here. The full-runtime lane owns PostgreSQL outage, disk-full, and real SIGTERM checks.

Written by failure_recovery_qa (gpt-6.1-sol/Codex)
on behalf of Daniel Drizhuk

