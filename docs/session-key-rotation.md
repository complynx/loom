# Session encryption key rotation

The active key is `identity.encryption_key_id` / `encryption_key_file`.
`previous_encryption_keys` maps other key IDs to secret file paths. All keys must
contain 32 raw bytes or their base64 encoding. Key IDs are immutable: never reuse
an ID for new key material. No keys enter the database or public settings API.

## Rolling rotation

1. Back up the database and all key versions separately. Keep access controls on
   both. A database backup still needs the keys that encrypted its envelopes.
2. Generate a new key in your secret store. Add it to `previous_encryption_keys`
   on every replica while keeping the old key active. Restart all replicas. They
   now read both versions and still write the old version.
3. Set the new key as active and put the old key in `previous_encryption_keys`.
   Restart every writer. Wait until no process with the old active configuration
   remains. Do not migrate before this barrier: an old writer can create another
   old envelope after a migration reports zero.
4. Run the operator command using the same configuration and both key files:

   ```sh
   loom rotate-session-keys --config config.yaml --batch-size 100
   ```

   Each invocation commits at most 100 envelopes (allowed range 1..1000). Repeat
   until `rotated_sessions=0`. SIGINT/SIGTERM cancels the current transaction;
   earlier committed batches survive. Retrying resumes from remaining records.
5. Remove the old key from the live configuration only after all writers use the
   new key and migration reports zero. Startup refuses any missing key referenced
   by the current database, including expired and revoked sessions. Keep the old
   key in protected backup storage until every backup needing it has expired.

The command contacts PostgreSQL only. It does not dispatch runs, refresh tokens,
call Zitadel, or contact a model. Use an operator database role with SELECT/UPDATE
on `auth_sessions`. Existing statement and lock timeouts also apply to rotation.
A batch fails atomically on an unknown key, changed identity/client binding, or
corrupt envelope. Fix the source problem; do not bypass authenticated decryption.

Rotation preserves identity, actor, organization, expiry, revocation state and
client-visible session version. Concurrent reauthorization and revocation are
serialized by row locks. Ordinary authentication still rejects expired sessions.
Changing the issuer, audience or OAuth client is a separate migration.

## Backup recovery

Restore the key versions from the time of the database backup before starting the
service or running rotation. The startup guard checks the live database; it cannot
prove that historical backups or still-running writers no longer need an old key.
Use [recovery mode](recovery-mode.md) and rehearse the complete backup restore
procedure. Restoring a backup must not silently dispatch uncertain external
operations.
