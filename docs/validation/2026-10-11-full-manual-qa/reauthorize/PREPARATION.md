# Real issuer and queue restart QA preparation

Isolated fixture prefix: `clx-fullqa-reauth-20261011`. PostgreSQL and Zitadel 4.16.3 are real containers. A local HTTPS reverse proxy forwards discovery, JWKS, introspection and token endpoints to Zitadel. Only `/v1/responses` and input-token counting use deterministic model responses. No OpenAI credentials or paid calls are used.

Ports: API 58480 (not started before review gate), issuer/proxy 58490, acceptance service 58491. Issuer is `https://peers:9443` within the isolated Docker network. TLS forwarding explicitly preserves the secure issuer. Fixture-only human token exchange uses the authorized impersonator machine; this is not production guidance and does not substitute for browser PKCE acceptance.

Private files: `management.local/full-qa-reauthorize/`. The directory contains issuer PATs, OAuth secrets, access tokens, encryption key and TLS key. It is ignored. Existing Zitadel and OpenAI stands are not changed.

Acceptance plan after the code review gate:

1. Create an explicit auth session and a question-producing run through the full HTTP API/River runtime.
2. Deactivate the actual owner in Zitadel. Prove old token returns 401. Reactivate and issue fresh real credentials.
3. Submit the question answer using fresh credentials. Stored revoked credentials must produce `waiting_auth`, without additional inference charge.
4. Restart the API process, including River workers. Prove durable same-run `waiting_auth` remains.
5. Test same-organization other-user access, foreign organization access and stale session-version conflict.
6. Reauthorize the same saved auth session with fresh owner credentials. Prove the same run completes, two model steps settle once each, and reserved credits return to zero.

No acceptance claim is made by preparation alone. No model tool side effect is scheduled in this lane; other module QA lanes cover mutation reconciliation.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
