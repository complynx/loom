-- +goose Up
-- Unknown historical identities are excluded until authenticated or explicitly opted in.
ALTER TABLE billing_accounts ADD COLUMN monthly_service_account boolean;
CREATE TABLE monthly_funding_policies (
 owner_id uuid PRIMARY KEY REFERENCES users(id),
 doc jsonb NOT NULL,
 version bigint NOT NULL CHECK(version>0)
);
CREATE TABLE monthly_funding_receipts (
 owner_id uuid NOT NULL REFERENCES users(id),
 period date NOT NULL,
 doc jsonb NOT NULL,
 PRIMARY KEY(owner_id,period)
);
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM monthly_funding_receipts) THEN
  RAISE EXCEPTION 'retain monthly funding receipts; disable funding and roll back binaries only';
 END IF;
END $$;
-- +goose StatementEnd
DROP TABLE monthly_funding_receipts, monthly_funding_policies;
ALTER TABLE billing_accounts DROP COLUMN monthly_service_account;
