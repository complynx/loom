# Manual dependency license review

License review is a periodic agent/operator task. It is not a merge or Dependabot
CI gate. Keep the existing approved license policy; review new direct dependencies
before selecting them and review accumulated transitive changes periodically.

The dated directories are historical evidence. `current/manifest.json` identifies
the exact dependency graph and bytes of the last generated snapshot. It can lag
behind the application lock after dependency updates. The directory name does not
mean CI has reviewed the current application graph.

For a review:

1. Verify the current module lock with `make lock-check`.
2. Install the go-licenses version pinned in tools/dependencies.lock.json. Set
   GO_LICENSES_BINARY if it is not on PATH.
3. Run `make audit-update` to generate Linux runtime/test reports, root-license
   evidence and the notice delta. If it stops on an unknown license SHA-256,
   inspect those upstream bytes and record the reviewed classification in a new
   dated module-root-licenses.json snapshot, using the existing report format.
   Review all newly encountered license files, not only the first one reported.
4. Rerun the update, inspect the reports and notices, then run `make audit-check`.
   Commit the reviewed snapshot. Do not rewrite older dated evidence.

Both images include historical notices and the latest reviewed current notice
delta. License coverage between reviews is an operator responsibility. The
file-worker's existing corresponding-source and checksum packaging checks remain
part of its build.

Written by Codex (gpt-6/Codex desktop), developer of Loom
on behalf of Daniel Drizhuk
