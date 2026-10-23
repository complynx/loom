-- +goose Up
-- Text and provenance are durable; temporary artifact bytes keep their existing deadlines.
CREATE TABLE audio_transcripts (
 id uuid PRIMARY KEY REFERENCES steps(id),
 owner_id uuid NOT NULL REFERENCES users(id),
 org_id text NOT NULL,
 source_artifact_id uuid NOT NULL REFERENCES artifacts(id),
 processing_key text NOT NULL CHECK(length(processing_key)=64),
 reprocess_key text NOT NULL DEFAULT '',
 dispatched_at timestamptz,
 result jsonb,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(owner_id,org_id,processing_key,reprocess_key)
);

-- +goose Down
DROP TABLE audio_transcripts;
