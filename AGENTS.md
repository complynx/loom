# Agent instructions

## Sign development messages

When an agent writes a commit message, PR/MR description, GitHub issue, or issue/PR discussion or review comment, end the generated text with:

```text
Written by <agent-name> (<model-slug>/<harness>), developer of Loom
on behalf of Daniel Drizhuk
```

Use the actual agent, model and harness names. Do not invent an identity. Follow an explicit user-supplied text or signature format when provided. Do not post externally without user authorization.

## Do not sign documentation

Do not add sign-off or agent-attribution footers to README, CONTRIBUTING, guides, examples, reference documentation or built-in help. The template above is a message requirement, not a documentation footer. Preserve required third-party license and copyright notices.

## Maintain documentation

Follow [CONTRIBUTING](CONTRIBUTING.md). Update the affected documentation with behavior/configuration/contract changes. Use focused technical pages and practical STE. Prefer clear precise wording when strict STE reduces readability.

Before opening a PR/MR, complete an independent review loop. After opening it, wait for required CI and resolve valid bot/reviewer findings.
