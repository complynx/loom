# Service accounts

A service account (SA) runs tasks under its own Zitadel subject. It owns its chats,
artifacts and billing balance. It does not need a delegated human token or a
business-user record in an unrelated service.

Configure the subject as a machine and explicitly enable self-access:

```yaml
identity:
  service_subjects: [descriptor-subject]
  service_accounts:
    descriptor-subject: [runs.create, runs.control, chat.read, artifacts.read, artifacts.write, media.access, media.read]
  role_permissions:
    descriptor: [runs.create, runs.control, chat.read, artifacts.read, artifacts.write, media.access, media.read]
```

Merge these fields into the existing identity configuration. The issuer,
audience, organization and token verification rules remain unchanged. Assign the
`descriptor` role to that machine in the appropriate Zitadel organization.
The `service_accounts` list is a permission ceiling, not a grant: effective rights
are its intersection with the verified subject's mapped IAM roles. Use exact
permission names, including the module-level and method-level permissions required
by the target service manifest. No wildcard is implied.

Send `Authorization: Bearer <machine-access-token>` to the normal `/v1` API.
Do not send `X-Subject-Token`. These accounts cannot impersonate another subject
or be used as a delegated user. Existing registered service callers retain their
separate human-delegation contract.

## Service calls and run restrictions

In an ordinary run, an SA can use native files and authorized service reads and
writes. The registry checks module permissions before dynamic discovery and
method permissions before calls. A module without the required module permission
is not visited for method discovery. An authorized module that is unavailable
still fails normally; authorization does not suppress dependency failures.
Dynamic discovery must support the service subject for modules intentionally
granted to that account.

Writes retain preparation, consent, idempotency, current authorization and
reconciliation. A target service receives the SA's credential and must explicitly
authorize that identity. Loom does not bypass the target's business rules.
Module discovery cannot prefilter a method-level permission before the module
returns its dynamic catalog: use distinct module-level permissions when different
accounts must discover different business areas.

The optional [native file profile](execution-profiles.md) further restricts a run
to native file tools, even if the SA has service permissions. Omit it when that
run needs authorized service calls. Service accounts have no personal-memory
API or tools, and personal micro-memory is not injected into their model requests.
Chats retain their own history. Use separate accounts/chats for separate tasks.

## Sessions, budgets and revocation

Run creation saves the verified credential in the existing encrypted authorization
session. Workers reverify that credential and apply the current permission ceiling
after restart. Token expiry still requires reauthorization; automatic machine-token
refresh is not added. Existing cache TTLs, disclosure policy, artifact ownership,
quotas and model budgets apply. Billing uses the SA's own internal account.

Keep every configured SA subject in `service_subjects`. To disable self-access,
remove it from `service_accounts` but retain the machine marker in
`service_subjects`; this prevents fallback to ordinary human self-access. To revoke
individual capabilities, remove them from the ceiling or IAM role. Configuration
changes require restarting all API and execution workers consistently; IAM changes
follow the existing authorization cache policy. Never remove the historical machine
marker while its credentials can still be valid.

This feature does not create identities or credentials in Zitadel. Provisioning
and token acquisition remain the caller/operator's responsibility.
