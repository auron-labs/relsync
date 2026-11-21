-- +goose Up
-- The user's standing decision to accept an unsigned program from this Item
-- without being asked. It never excuses a bad checksum or an invalid signature.
ALTER TABLE items ADD COLUMN trust_unsigned INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE items DROP COLUMN trust_unsigned;
