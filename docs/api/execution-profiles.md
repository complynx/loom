# Execution profiles

An execution profile restricts one run. It does not grant permissions or change
the authenticated identity, delegation, ownership, disclosure policy or budget.
Omission of `execution_profile` keeps the existing run behavior.

## Native file runs

Before using this contract, call authenticated `GET /v1/capabilities` with
`chat.read`. Require `execution_profiles` to contain `native_files`. Do not fall
back to an unrestricted run when that value is absent.

Create a separate chat for the descriptor, upload the original file under the
dedicated subject, then send `POST /v1/runs` with a persisted `Idempotency-Key`:

```json
{
  "execution_profile": "native_files",
  "chat_id": "<descriptor-chat-id>",
  "input": {
    "text": "Inspect original TGS artifact <owned-artifact-id>. Use native file tools and return a short JSON description.",
    "artifact_ids": []
  },
  "budget": {"max_microcredits": 1000000}
}
```

Use a [service account](service-accounts.md) or the existing
[authentication and delegation](authentication-routes.md) contract.
It does not require the subject to have an account in a discovered business
service. No business-method discovery or RPC is performed for this run.
Deployment-level manifest polling and caller registration remain active.

The run exposes `script`, `artifact_attach` and `question`. Script bindings contain
native `files` methods (except `transcript_read`), `artifacts.open`, and
`help.list`/`help.read`. File tools still require their deployment switch and
current `chat.read` and `artifacts.read` permissions. File operations may create
owned derived artifacts, such as timeline frames. Existing quotas and limits apply.

Service reads and writes, `module_call`, external artifact fetch/refetch,
diagnostics, history tools, personal-memory tools, paid transcription, reactions
and action-resolution tools are unavailable. Dispatch enforces the restriction
even if a model invents a tool name. Help lists only the permitted builtins.
Personal micro-memory is not read or injected into inference, summaries or
compaction requests.

The profile does not erase existing chat history or restrict uploads to only the
IDs named in the prompt. Use a dedicated subject and a separate chat for data
isolation. Native file access retains the subject's ownership checks. Upload
original bytes before creating the run: artifacts backed by external source
references cannot be opened in this profile, even when cached bytes still exist.
Expired uploads cannot be restored through a service.

For TGS, put the opaque artifact ID in input text. `input.artifact_ids` requests
direct model attachments and is not a general file-input list. The model opens
the artifact, uses `files.video_info`, `files.video_storyboard` or
`files.video_frame`, then attaches selected still frames. See [file tools](../file-tools.md).
The profile does not enforce a JSON response schema; normal output limits and
output validation continue to apply.

## Persistence and compatibility

The server stores `execution_profile` in the run document and returns it in run
responses. It is immutable for that run, including steering and restart. Changing
it on an idempotent creation retry returns `409 idempotency_conflict`. Unknown
nonempty profiles return `422 invalid_execution_profile`. Cached context prefixes
are not reused between different profiles.

Upgrade all API and execution workers before advertising or using this profile.
Older APIs reject the request field; older workers do not enforce it in saved run
documents. Mixed execution-worker versions are unsupported. Before rollback,
stop admission and finish or cancel all nonterminal runs with this profile, then
stop all upgraded workers before starting older binaries. No SQL migration is
needed because run documents already store immutable run options as JSON.

## Verification

Run `TestNativeFilesSkipsDeniedDiscoveryAndRejectsEscape` for discovery and
dispatch restrictions. With disposable PostgreSQL, run
`TestExecutionProfilePersistsAcrossRetryAndWorkerRestart` for persisted authority.
Run `TestNativeFilesTGSRunWithUnavailableBusiness` in the `file-runtime-tests`
image with disposable PostgreSQL. It uses a dedicated service-account principal, a
registered business module whose discovery returns 403, synthetic TGS frames and
a local counted model. The run must complete and attach a frame with zero
business requests and no personal-memory injection. No paid calls are used.
