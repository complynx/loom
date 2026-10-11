-- +goose Up
-- 00001_core
CREATE TABLE settings (singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton), doc jsonb NOT NULL, version bigint NOT NULL DEFAULT 1);
CREATE TABLE users (id uuid PRIMARY KEY, issuer text NOT NULL, subject text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(issuer,subject));
CREATE TABLE memberships (owner_id uuid NOT NULL REFERENCES users(id), org_id text NOT NULL, PRIMARY KEY(owner_id,org_id));
CREATE TABLE user_preferences (owner_id uuid PRIMARY KEY REFERENCES users(id), doc jsonb NOT NULL, version bigint NOT NULL DEFAULT 1);
CREATE TABLE user_policies (owner_id uuid NOT NULL REFERENCES users(id), org_id text NOT NULL, doc jsonb NOT NULL, version bigint NOT NULL DEFAULT 1, PRIMARY KEY(owner_id,org_id));
CREATE TABLE billing_accounts(owner_id uuid PRIMARY KEY REFERENCES users(id), balance bigint NOT NULL CHECK(balance>=0), spent bigint NOT NULL DEFAULT 0 CHECK(spent>=0), reserved bigint NOT NULL DEFAULT 0 CHECK(reserved>=0), CHECK(balance>=reserved));
CREATE TABLE chats(id uuid PRIMARY KEY,owner_id uuid NOT NULL REFERENCES users(id),org_id text NOT NULL,is_default boolean NOT NULL DEFAULT false,next_seq bigint NOT NULL DEFAULT 1,created_at timestamptz NOT NULL DEFAULT now());
CREATE UNIQUE INDEX one_default_chat ON chats(owner_id) WHERE is_default;
CREATE TABLE auth_sessions(id uuid PRIMARY KEY,issuer text NOT NULL,subject text NOT NULL,actor text NOT NULL,org_id text NOT NULL,envelope jsonb NOT NULL,expires_at timestamptz NOT NULL,version bigint NOT NULL DEFAULT 1,revoked_at timestamptz);
CREATE TABLE runs(id uuid PRIMARY KEY,chat_id uuid NOT NULL REFERENCES chats(id),owner_id uuid NOT NULL REFERENCES users(id),actor_id text NOT NULL,org_id text NOT NULL,state text NOT NULL CHECK(state IN('queued','running','waiting_input','waiting_approval','waiting_auth','reconciling','paused','completed','cancelled','failed')),reason text NOT NULL DEFAULT '',doc jsonb NOT NULL,lease_owner text NOT NULL DEFAULT '',lease_until timestamptz,fence bigint NOT NULL DEFAULT 0,version bigint NOT NULL DEFAULT 1,pause_requested boolean NOT NULL DEFAULT false,cancel_requested boolean NOT NULL DEFAULT false,next_seq bigint NOT NULL DEFAULT 1,spent_microcredits bigint NOT NULL DEFAULT 0,reserved_microcredits bigint NOT NULL DEFAULT 0,max_microcredits bigint NOT NULL CHECK(max_microcredits>0),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now());
CREATE UNIQUE INDEX one_active_run_per_chat ON runs(chat_id) WHERE state NOT IN('completed','cancelled','failed');
CREATE TABLE run_events(run_id uuid NOT NULL REFERENCES runs(id),seq bigint NOT NULL,type text NOT NULL,data jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(run_id,seq));
CREATE TABLE chat_events(chat_id uuid NOT NULL REFERENCES chats(id),seq bigint NOT NULL,kind text NOT NULL,text text NOT NULL,doc jsonb NOT NULL,run_id uuid REFERENCES runs(id),source_actor text NOT NULL,source_event_id text,request_hash text,occurred_at timestamptz NOT NULL DEFAULT now(),received_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(chat_id,seq));
CREATE UNIQUE INDEX unique_passive_event ON chat_events(source_actor,source_event_id) WHERE source_event_id IS NOT NULL;
CREATE INDEX chat_events_text_search ON chat_events USING gin(to_tsvector('simple',text));
CREATE TABLE idempotency_records(scope text NOT NULL,key text NOT NULL,request_hash text NOT NULL,resource_id uuid NOT NULL,PRIMARY KEY(scope,key));
CREATE TABLE run_inputs(id uuid PRIMARY KEY,run_id uuid NOT NULL REFERENCES runs(id),doc jsonb NOT NULL,applied boolean NOT NULL DEFAULT false,created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE steps(id uuid PRIMARY KEY,run_id uuid NOT NULL REFERENCES runs(id),ordinal bigint NOT NULL,kind text NOT NULL,state text NOT NULL,input jsonb NOT NULL,result jsonb,usage jsonb NOT NULL DEFAULT '{}',reserve bigint NOT NULL DEFAULT 0 CHECK(reserve>=0),created_at timestamptz NOT NULL DEFAULT now(),UNIQUE(run_id,ordinal));
CREATE TABLE credit_ledger(id uuid PRIMARY KEY,owner_id uuid NOT NULL REFERENCES users(id),run_id uuid REFERENCES runs(id),step_id uuid REFERENCES steps(id),settlement_key text NOT NULL UNIQUE,amount bigint NOT NULL,kind text NOT NULL,reason text NOT NULL DEFAULT '',created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE artifacts(id uuid PRIMARY KEY,owner_id uuid NOT NULL REFERENCES users(id),org_id text NOT NULL,mime text NOT NULL,sha256 text NOT NULL,size bigint NOT NULL CHECK(size>=0),created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE audit_events(id uuid PRIMARY KEY,actor text NOT NULL,org_id text NOT NULL,action text NOT NULL,target text NOT NULL,doc jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now());

-- 00002_operations
CREATE TABLE write_operations (
    id uuid PRIMARY KEY,
    run_id uuid NOT NULL REFERENCES runs(id),
    call_id text NOT NULL,
    state text NOT NULL CHECK (state IN ('preparing','prepared','approved','rejected','dispatching','pending','unknown','succeeded','failed')),
    doc jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(run_id, call_id)
);
CREATE INDEX write_operations_reconcile ON write_operations(updated_at)
    WHERE state IN ('dispatching','pending','unknown');

-- 00003_context
CREATE TABLE context_summaries(chat_id uuid PRIMARY KEY REFERENCES chats(id),through_seq bigint NOT NULL CHECK(through_seq>=0),text text NOT NULL,source_hash text NOT NULL,model_id text NOT NULL,created_at timestamptz NOT NULL DEFAULT now());

-- 00004_lifecycle
ALTER TABLE chats ADD COLUMN deleted_at timestamptz;
ALTER TABLE chats ADD COLUMN retention_days integer NOT NULL DEFAULT 0 CHECK(retention_days>=0);
ALTER TABLE artifacts ADD COLUMN deleted_at timestamptz;
ALTER TABLE artifacts ADD COLUMN retention_days integer NOT NULL DEFAULT 0 CHECK(retention_days>=0);
ALTER TABLE runs ADD COLUMN active_elapsed_ms bigint NOT NULL DEFAULT 0 CHECK(active_elapsed_ms>=0);
ALTER TABLE runs ADD COLUMN active_started_at timestamptz;
ALTER TABLE runs ADD COLUMN state_entered_at timestamptz NOT NULL DEFAULT clock_timestamp();
UPDATE runs SET state_entered_at=updated_at;
CREATE TABLE chat_artifacts(chat_id uuid NOT NULL REFERENCES chats(id),artifact_id uuid NOT NULL REFERENCES artifacts(id),PRIMARY KEY(chat_id,artifact_id));
CREATE TABLE passive_event_receipts(source_actor text NOT NULL,source_event_id text NOT NULL,request_hash text,chat_id uuid NOT NULL REFERENCES chats(id),seq bigint NOT NULL,PRIMARY KEY(source_actor,source_event_id));
INSERT INTO passive_event_receipts(source_actor,source_event_id,request_hash,chat_id,seq) SELECT source_actor,source_event_id,request_hash,chat_id,seq FROM chat_events WHERE source_event_id IS NOT NULL;
-- +goose StatementBegin
CREATE FUNCTION retain_passive_event_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.source_event_id IS NOT NULL THEN
  IF EXISTS(SELECT 1 FROM passive_event_receipts p WHERE p.source_actor=NEW.source_actor AND p.source_event_id=NEW.source_event_id AND p.request_hash IS NOT DISTINCT FROM NEW.request_hash AND p.chat_id=NEW.chat_id AND p.seq=NEW.seq) THEN RETURN NEW; END IF;
  INSERT INTO passive_event_receipts(source_actor,source_event_id,request_hash,chat_id,seq) VALUES(NEW.source_actor,NEW.source_event_id,NEW.request_hash,NEW.chat_id,NEW.seq);
 END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER passive_event_receipt AFTER INSERT OR UPDATE OF source_event_id,request_hash ON chat_events FOR EACH ROW EXECUTE FUNCTION retain_passive_event_receipt();
CREATE INDEX chat_artifacts_by_artifact ON chat_artifacts(artifact_id);
CREATE INDEX runs_lifecycle ON runs(state_entered_at) WHERE state NOT IN('completed','cancelled','failed');
-- +goose StatementBegin
CREATE FUNCTION track_run_state_entry() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.state IS DISTINCT FROM OLD.state THEN NEW.state_entered_at=clock_timestamp(); END IF;
 IF OLD.state='running' AND OLD.active_started_at IS NOT NULL AND (NEW.state<>'running' OR NEW.fence<>OLD.fence) THEN
  NEW.active_elapsed_ms=OLD.active_elapsed_ms+GREATEST(0,EXTRACT(EPOCH FROM (LEAST(clock_timestamp(),COALESCE(OLD.lease_until,clock_timestamp()))-OLD.active_started_at))*1000)::bigint;
 END IF;
 IF NEW.state='running' AND (OLD.state<>'running' OR NEW.fence<>OLD.fence) THEN NEW.active_started_at=clock_timestamp();
 ELSIF NEW.state<>'running' THEN NEW.active_started_at=NULL;
 END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER run_state_entry BEFORE UPDATE OF state,fence ON runs FOR EACH ROW EXECUTE FUNCTION track_run_state_entry();
-- Retention snapshots apply to future resources. Existing records retain the
-- safe forever default until an explicit migration changes their policy.
-- +goose StatementBegin
CREATE FUNCTION snapshot_retention_policy() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE policy jsonb;
BEGIN
 IF TG_TABLE_NAME='chats' THEN SELECT doc->'history_retention' INTO policy FROM settings WHERE singleton;
 ELSE SELECT doc->'artifact_retention' INTO policy FROM settings WHERE singleton;
 END IF;
 IF policy->>'mode'='duration' THEN NEW.retention_days=(policy->>'days')::int;
 ELSE NEW.retention_days=0;
 END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER chat_retention_snapshot BEFORE INSERT ON chats FOR EACH ROW EXECUTE FUNCTION snapshot_retention_policy();
CREATE TRIGGER artifact_retention_snapshot BEFORE INSERT ON artifacts FOR EACH ROW EXECUTE FUNCTION snapshot_retention_policy();

-- 00005_receipts
ALTER TABLE idempotency_records ADD COLUMN request_doc jsonb;
CREATE INDEX artifacts_owner_index ON artifacts(owner_id);

-- 00006_service_identities
CREATE TABLE service_identities(issuer text NOT NULL,subject text NOT NULL,audience text NOT NULL,first_seen_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(issuer,subject,audience));

-- 00007_metadata
ALTER TABLE artifacts ADD COLUMN external_disclosure_allowed boolean NOT NULL DEFAULT false;
ALTER TABLE artifacts ADD COLUMN source_service_id text NOT NULL DEFAULT '';
ALTER TABLE chat_events ADD COLUMN action text NOT NULL DEFAULT '';

-- 00008_artifact_collection
ALTER TABLE artifacts ADD COLUMN collected_at timestamptz;
CREATE INDEX artifacts_pending_collection ON artifacts(deleted_at,id) WHERE deleted_at IS NOT NULL AND collected_at IS NULL;

-- 00009_search_context
CREATE TABLE run_contexts (
 run_id uuid PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
 chat_id uuid NOT NULL REFERENCES chats(id),
 through_seq bigint NOT NULL,
 source_count bigint NOT NULL,
 items jsonb NOT NULL,
 expires_at timestamptz NOT NULL
);
CREATE INDEX run_context_expiry ON run_contexts(expires_at);
CREATE TABLE event_embeddings (
 chat_id uuid NOT NULL,
 seq bigint NOT NULL,
 model text NOT NULL CHECK(length(model) BETWEEN 1 AND 128),
 dimensions integer NOT NULL CHECK(dimensions BETWEEN 1 AND 4096),
 embedding jsonb NOT NULL CHECK(jsonb_typeof(embedding)='array'),
 PRIMARY KEY(chat_id,seq,model),
 FOREIGN KEY(chat_id,seq) REFERENCES chat_events(chat_id,seq) ON DELETE CASCADE
);
CREATE TABLE artifact_handles (
 id uuid PRIMARY KEY,
 run_id uuid NOT NULL REFERENCES runs(id),
 artifact_id uuid NOT NULL REFERENCES artifacts(id),
 owner_id uuid NOT NULL REFERENCES users(id),
 org_id text NOT NULL,
 actor_id text NOT NULL,
 expires_at timestamptz NOT NULL,
 UNIQUE(run_id,artifact_id)
);
CREATE INDEX artifact_handles_expiry ON artifact_handles(expires_at);

-- 00010_platform_attempts
CREATE TABLE platform_provider_attempts (
 step_id uuid PRIMARY KEY REFERENCES steps(id),
 run_id uuid NOT NULL REFERENCES runs(id),
 owner_id uuid NOT NULL REFERENCES users(id),
 org_id text NOT NULL,
 model_snapshot jsonb NOT NULL,
 client_request_id text NOT NULL,
 reserved_exposure bigint NOT NULL CHECK(reserved_exposure>=0),
 status text NOT NULL DEFAULT 'unknown' CHECK(status IN('unknown','settled')),
 usage jsonb,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX platform_provider_attempts_run ON platform_provider_attempts(run_id);

-- 00011_provider_retry
CREATE TABLE provider_retries (
 run_id uuid PRIMARY KEY REFERENCES runs(id),
 attempts integer NOT NULL CHECK (attempts>0),
 next_attempt_at timestamptz NOT NULL,
 deadline timestamptz NOT NULL,
 code text NOT NULL CHECK (code IN ('rate_limit_exceeded','rate_limit','slow_down','server_is_overloaded'))
);

-- 00012_user_memory
CREATE TABLE user_memory (
 owner_id uuid NOT NULL REFERENCES users(id),
 org_id text NOT NULL CHECK(org_id<>''),
 micro text NOT NULL DEFAULT '' CHECK(char_length(micro)<=500),
 micro_version bigint NOT NULL DEFAULT 0,
 memory_index text NOT NULL DEFAULT '' CHECK(char_length(memory_index)<=8000),
 index_version bigint NOT NULL DEFAULT 0,
 revision bigint NOT NULL DEFAULT 0,
 PRIMARY KEY(owner_id,org_id)
);
CREATE TABLE user_memory_entries (
 owner_id uuid NOT NULL,
 org_id text NOT NULL,
 key text NOT NULL CHECK(key ~ '^[a-z0-9][a-z0-9_-]{0,79}$'),
 kind text NOT NULL CHECK(kind IN ('note','source')),
 parent_key text NOT NULL DEFAULT '',
 title text NOT NULL CHECK(char_length(title)<=200),
 summary text NOT NULL CHECK(char_length(summary)<=1000),
 body text NOT NULL CHECK(char_length(body)<=64000),
 version bigint NOT NULL CHECK(version>0),
 PRIMARY KEY(owner_id,org_id,key),
 FOREIGN KEY(owner_id,org_id) REFERENCES user_memory(owner_id,org_id) ON DELETE CASCADE
);

-- +goose Down
-- 00012_user_memory
DROP TABLE user_memory_entries;
DROP TABLE user_memory;

-- 00011_provider_retry
DROP TABLE provider_retries;

-- 00010_platform_attempts
DROP TABLE platform_provider_attempts;

-- 00009_search_context
DROP TABLE artifact_handles;
DROP TABLE event_embeddings;
DROP TABLE run_contexts;

-- 00008_artifact_collection
DROP INDEX artifacts_pending_collection;
ALTER TABLE artifacts DROP COLUMN collected_at;

-- 00007_metadata
ALTER TABLE chat_events DROP COLUMN action;
ALTER TABLE artifacts DROP COLUMN source_service_id;
ALTER TABLE artifacts DROP COLUMN external_disclosure_allowed;

-- 00006_service_identities
DROP TABLE service_identities;

-- 00005_receipts
DROP INDEX artifacts_owner_index;
ALTER TABLE idempotency_records DROP COLUMN request_doc;

-- 00004_lifecycle
DROP TRIGGER passive_event_receipt ON chat_events;
DROP FUNCTION retain_passive_event_receipt();
DROP TABLE passive_event_receipts;
DROP TRIGGER chat_retention_snapshot ON chats;
DROP TRIGGER artifact_retention_snapshot ON artifacts;
DROP FUNCTION snapshot_retention_policy();
DROP TRIGGER run_state_entry ON runs;
DROP FUNCTION track_run_state_entry();
DROP INDEX runs_lifecycle;
DROP TABLE chat_artifacts;
ALTER TABLE runs DROP COLUMN state_entered_at;
ALTER TABLE runs DROP COLUMN active_started_at;
ALTER TABLE runs DROP COLUMN active_elapsed_ms;
ALTER TABLE artifacts DROP COLUMN deleted_at;
ALTER TABLE artifacts DROP COLUMN retention_days;
ALTER TABLE chats DROP COLUMN deleted_at;
ALTER TABLE chats DROP COLUMN retention_days;

-- 00003_context
DROP TABLE context_summaries;

-- 00002_operations
DROP TABLE write_operations;

-- 00001_core
DROP TABLE audit_events,artifacts,credit_ledger,steps,run_inputs,idempotency_records,chat_events,run_events,runs,auth_sessions,chats,billing_accounts,user_policies,user_preferences,memberships,users,settings;
