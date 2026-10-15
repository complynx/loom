# Manual dependency license review

Review licenses before selecting new direct dependencies and periodically for
transitive updates. This is an agent/operator task, not a CI or Dependabot gate.
The approved license policy remains unchanged.

1. Read the versions in `go.mod` and the selected graph from `go list -m all`.
2. Inspect upstream license files for new or changed modules. Include application,
   test and development-tool dependencies, noting their different distribution scopes.
3. Record findings in a dated report and preserve required license notices. Review
   native packages and corresponding-source obligations separately.
4. Update the redistributed notices when needed and verify the image contents.

Dated directories and `current/manifest.json` are historical evidence of the graph
and bytes reviewed at that time. They do not certify later dependency updates.
Both Docker images retain reviewed notices; the file-worker also ships its
corresponding-source material. Existing source/checksum packaging checks remain
part of its build.

Written by Codex (gpt-6/Codex desktop), developer of Loom
on behalf of Daniel Drizhuk
