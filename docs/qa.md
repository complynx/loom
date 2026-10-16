# Quality assurance

Test observable behavior. Prefer happy paths and critical paths over assertions about internal structure. A running installation alone does not prove that a feature meets its acceptance criteria.

| Task | Guide |
| --- | --- |
| Prepare a stand and run code checks | [Setup and gates](qa/setup.md) |
| Test clients, identities and actions | [API and conversation](qa/conversation.md) |
| Test files, usage and context | [Files and accounting](qa/files-accounting.md) |
| Test failures and service integration | [Recovery and integration](qa/recovery.md) |

Start with [setup and gates](qa/setup.md). Each scenario page includes focused
fixture commands and a separate checklist for installation checks. Use real
PostgreSQL, Zitadel, media tools or service clients for the boundary under test.
Keep paid calls and destructive faults out of routine tests.

Store run reports, receipts, screenshots and dumps in private CI artifacts or ignored local storage. Keep reproducible tests and instructions in the repository. Dependency notices and corresponding-source materials are distribution requirements, not QA reports.

Before completion, check that unrelated data and resources are unchanged, remove only your own temporary containers, and remove secrets from evidence. Update [limitations](mvp-limitations.md) and the [track](development-track.md) only for the verified scope. Code checks, local functional QA, live-model quality and installation acceptance are different results.
