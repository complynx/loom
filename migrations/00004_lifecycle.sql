-- +goose Up
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
-- +goose Down
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
