-- Metadata fixtures only. No queue, model steps, or execution jobs exist.
-- Execute only against the dedicated disposable panelqa database.
BEGIN;
INSERT INTO users(id,issuer,subject) VALUES ('00000000-0000-0000-0000-000000000001','http://localhost:58390','qa-pagination-fixture');
INSERT INTO chats(id,owner_id,org_id) SELECT ('10000000-0000-0000-0000-'||lpad(n::text,12,'0'))::uuid,'00000000-0000-0000-0000-000000000001',:'org_id' FROM generate_series(1,51) n;
INSERT INTO runs(id,chat_id,owner_id,actor_id,org_id,state,doc,max_microcredits) SELECT ('20000000-0000-0000-0000-'||lpad(n::text,12,'0'))::uuid,('10000000-0000-0000-0000-'||lpad(n::text,12,'0'))::uuid,'00000000-0000-0000-0000-000000000001','qa-pagination-fixture',:'org_id','paused','{}',1 FROM generate_series(1,51) n;
COMMIT;
-- Written by Codex (gpt-6/Codex desktop)
-- on behalf of Daniel Drizhuk
