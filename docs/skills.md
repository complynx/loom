# Skills

Skills describe how to complete a task with existing capabilities. They use the
existing `script` tool. Loading a skill does not execute code or grant authority.

## Definition

```json
{
  "name": "inspect-document",
  "usecase": "Inspect document evidence and cite the relevant pages.",
  "require": ["files.pdf_text", "files.pdf_render"],
  "body": "Read the relevant pages. Render pages when layout matters. Cite the evidence. Put examples and expected output here."
}
```

`name` has at most 64 ASCII characters: lowercase letters and digits separated
by single hyphens. `usecase` is nonblank and has at most 1,024 Unicode characters.
`body` is nonblank Markdown with at most 16,000 Unicode characters. Examples,
expected output and other guidance belong in `body`.

`require` is optional. It contains at most 50 unique `module.method` IDs. Every
required method must be authorized before the skill appears in model context,
model skill listing or model skill reads. This checks capability availability, not whether a
business resource currently exists. Visibility does not change authorization:
a hidden but authorized method satisfies a dependency. Requirements never grant
permissions or consent. Builtin method IDs from help can also be required.

## Sources

Loom combines four sources without allowing one to overwrite another:

| Source | Publication | Lifetime |
| --- | --- | --- |
| `builtin` | Procedures bundled with Loom | Installed binary, version 1. |
| `organization` | Skills HTTP API or `modules.skills.put` | Persistent organization library. |
| `caller` | Optional `skills` array in `POST /v1/runs` | Immutable run snapshot, restored after restart. |
| `tool:<module>/<method>` | Optional `skills` array on a method descriptor | Current authorized static manifest or dynamic discovery. |

Caller requests and each method can supply at most 20 definitions. Supplied
definitions have `name`, `usecase`, optional `require`, and `body`; they cannot
set source or library version. Caller skills are included in request idempotency.
They do not edit the organization library and need only normal run admission
rights. Tool skills are visible only while their publishing method is authorized.

Loom supplies `source`. Builtin skills have a bundled positive version; they cannot be changed through the organization API. Organization skills have a positive monotonic
`version`; caller and tool skills use version zero. Edit those definitions at
their source. They are procedural guidance and cannot override system rules,
current user intent, execution profiles, disclosure rules or consent. The
`native_files` profile excludes skills and rejects caller skill definitions.

Method visibility is separate from skill availability. A hidden authorized
method can advertise a skill whose usecase is visible immediately; the skill can
tell the model to read that method's exact contract. See
[discovery visibility](modules/authoring-help.md#discovery-visibility).

## Model interface

Every inference advertises all currently available skill metadata in the
`script` tool description: `name`, `source`, `usecase`, `require`, and `version`.
Bodies stay out of this initial catalog. Existing context input limits apply;
Loom does not silently drop skill metadata to fit a request.

```js
return await modules.skills.read({name: "inspect-document", source: "caller"});
```

`read` defaults `source` to `organization`. Copy the source from the catalog for
caller or tool skills. The result includes the full body. Names can repeat across
sources; they never override each other.

`modules.skills.list({query, after_name, limit})` refreshes or searches metadata.
All fields are optional. Query is a case-insensitive literal substring of name
and usecase. The default limit is 20, maximum 100. Copy `next_after_name` unchanged
to `after_name` with the same query while `partial` is true. This script cursor
contains source/name; it differs from the organization-only HTTP cursor.

Reads and listing recheck current authority and dependencies. Missing, deleted
or dependency-inaccessible skills return `not_found`. Loaded bodies remain in
existing context until compaction; revoking access does not erase earlier text.
The broker still checks current authority before each effect. Reload a skill
when its full instructions are needed after compaction.

## Builtin procedures and discovery

Builtin skills are read with `modules.skills.read({name,source:"builtin"})`.
Their names and usecases appear immediately when all requirements are available.
Bodies load only on demand. A same-named organization, caller or tool skill does
not replace a builtin. Bundled procedure changes require a Loom release; bump
the bundled version when changing their instructions.

| Skill | Procedure | Method discovery |
| --- | --- | --- |
| `manage-skills` | Authorized organization-library edits, version conflicts and verification. | `skills.put/delete` are hidden. `skills.list/read` remain visible. |
| `manage-memory` | Explicit personal-memory maintenance, bounded reads and versioned edits. | Memory editing methods are hidden; state/list/read remain visible. |
| `inspect-documents` | Evidence-based PDF, Office, text, EPUB and archive inspection. | Specialized file methods appear in normal lists but not the initial overview. |
| `inspect-media` | Still images, animation/video frames and optional audio evidence. | Specialized file methods appear in normal lists but not the initial overview. |
| `diagnose-run` | Read-only diagnosis of waits, errors and unknown effects. | Diagnostic methods require explicit search or a known ID. |

The management skill requires `skills.put` and `skills.delete`, so users without
`skills.write` do not see it. Service accounts never see personal-memory
management. File skills require enabled file tools and artifact-read rights;
diagnostics require diagnostic authority. Skills do not acquire missing rights.

These builtin visibility values are part of their help cards. Hidden contracts
remain readable through direct authorized `modules.help.read({module,method})`.
All methods retain the same dispatch, authorization and recovery rules.
`files.info`, artifact opening, history search, help and skill reading remain
easy to discover. Native model tools for attachments, questions, consent,
external writes and paid transcription retain their existing execution paths.

## Library editing

Reading the library requires `chat.read`. Creating, updating or deleting library
skills requires **both `chat.read` and `skills.write`**. Configure the latter in
the editor role's `identity.role_permissions`. The example configuration grants
it to `harness-admin`; it is not implied by other permissions. Every HTTP or
script edit uses fresh authorization, bypassing the positive authority cache.
Script methods are also hidden from help/bindings without the write permission,
and direct dispatch checks it again. Organization is taken from authenticated
identity, never from submitted fields.

| HTTP operation | Request/result |
| --- | --- |
| `GET /v1/skills?query=&after_name=&limit=20` | Library metadata, `partial`, `next_after_name`. HTTP cursor is the last name. |
| `GET /v1/skills/{name}` | Full organization skill. |
| `PUT /v1/skills/{name}` | `{usecase, body, require?, version}`; returns full updated skill. |
| `DELETE /v1/skills/{name}` | `{version}`; returns 204. |

The equivalent script methods are `modules.skills.put({name,usecase,body,require?,version})`
and `modules.skills.delete({name,version})`. Deletion returns null. No separate
model tool is introduced. Only edit when the user authorizes skill maintenance;
possession of a permission does not supply user intent.

Version zero creates or recreates a skill. Updating and deleting require its
current positive version. `skill_version_conflict` means reread and resolve the
edit; do not blindly retry. Versions do not repeat after deletion/recreation.
At most 100 active library skills are allowed per organization (`skill_limit`).
Deletion clears current text and requirements, retaining a version tombstone.
Earlier model context, logs and backups can retain copies.

The library is stored by migration `00008_skills.sql`. It adds a table and does
not rewrite existing history or run state. Old binaries ignore the new table;
stop newer workers before reverting. Caller skills in stored run documents need
a compatible binary to remain available. No downgrade migration runs at startup.

There is no filesystem skill installer, automatic code execution, bundled-file
loader or background skill extraction. Skill instructions are Markdown stored
and read through the protocol.
