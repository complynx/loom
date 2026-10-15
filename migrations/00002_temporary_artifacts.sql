-- +goose Up
-- Existing blobs receive a finite deadline from their original creation time.
-- Metadata, history, and financial receipts are retained after collection.
ALTER TABLE artifacts ADD COLUMN expires_at timestamptz;
UPDATE artifacts SET expires_at=created_at+interval '24 hours';
ALTER TABLE artifacts ALTER COLUMN expires_at SET NOT NULL;
ALTER TABLE artifacts ALTER COLUMN expires_at SET DEFAULT (now()+interval '24 hours');
ALTER TABLE artifacts ADD COLUMN source_external_id text NOT NULL DEFAULT '';
ALTER TABLE artifacts ADD CONSTRAINT artifacts_finite_lifetime CHECK(expires_at<=created_at+interval '7 days');
CREATE INDEX artifacts_expiry_index ON artifacts(expires_at) WHERE deleted_at IS NULL;

-- Remove file copies from legacy compaction and JS byte-read checkpoints.
-- Financial usage, source metadata, text, and opaque compaction context remain.
-- +goose StatementBegin
CREATE FUNCTION loom_omit_checkpoint_files(value jsonb) RETURNS jsonb LANGUAGE plpgsql AS $$
DECLARE result jsonb; item record; parsed jsonb; text_value text;
BEGIN
 IF jsonb_typeof(value)='object' THEN
  IF value->>'type' IN ('input_image','input_file','input_audio') THEN
   RETURN '{"type":"input_text","text":"Temporary file omitted from checkpoint. Fetch again from source service."}'::jsonb;
  END IF;
  IF value->>'encoding'='base64' AND value ? 'metadata' AND value ? 'data' THEN
   RETURN (value-'data') || '{"encoding":"expired","file_expired":true}'::jsonb;
  END IF;
  result='{}'::jsonb;
  FOR item IN SELECT key,val FROM jsonb_each(value) AS entry(key,val) LOOP
   result=result || jsonb_build_object(item.key,loom_omit_checkpoint_files(item.val));
  END LOOP;
  RETURN result;
 ELSIF jsonb_typeof(value)='array' THEN
  SELECT COALESCE(jsonb_agg(loom_omit_checkpoint_files(element) ORDER BY ordinal),'[]'::jsonb)
  INTO result FROM jsonb_array_elements(value) WITH ORDINALITY AS entries(element,ordinal);
  RETURN result;
 ELSIF jsonb_typeof(value)='string' THEN
  text_value=value#>>'{}';
  IF left(ltrim(text_value),1) IN ('{','[') THEN
   BEGIN
    parsed=text_value::jsonb;
   EXCEPTION WHEN invalid_text_representation THEN RETURN value;
   END;
   result=loom_omit_checkpoint_files(parsed);
   IF result<>parsed THEN RETURN to_jsonb(result::text); END IF;
  END IF;
 END IF;
 RETURN value;
END;
$$;
-- +goose StatementEnd
UPDATE steps SET result=loom_omit_checkpoint_files(result)
WHERE kind IN ('compact','tool','model') AND result IS NOT NULL
AND result<>loom_omit_checkpoint_files(result);
UPDATE run_contexts SET items=loom_omit_checkpoint_files(items)
WHERE items<>loom_omit_checkpoint_files(items);
DROP FUNCTION loom_omit_checkpoint_files(jsonb);

-- +goose Down
DROP INDEX artifacts_expiry_index;
ALTER TABLE artifacts DROP CONSTRAINT artifacts_finite_lifetime;
ALTER TABLE artifacts DROP COLUMN source_external_id;
ALTER TABLE artifacts DROP COLUMN expires_at;
