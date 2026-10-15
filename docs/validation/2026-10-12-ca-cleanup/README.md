# Test CA cleanup follow-up — 2026-10-12

After the earlier user-assisted browser fixture acceptance, the test CA was removed. A fresh read-only check under the operator account found zero matching certificates in both Cert:/CurrentUser/Root and Cert:/LocalMachine/Root for fingerprint 9863e05ced87904a745defebef2a906435f36a78. No certificate was installed, removed or changed by this follow-up check.

The [original browser acceptance report](../2026-10-12-browser-qa/README.md) is unchanged and describes trust at its earlier acceptance time. Its statement that trust remains is historical; current installed Windows root trust no longer includes this fixture CA. This follow-up checks the stores only, not a new browser login or TLS request. The new local fixture clients use an explicitly mounted private CA, separate from Windows root trust.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
