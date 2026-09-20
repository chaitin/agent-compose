-- Explicit credential presentation for an API-managed connection.
--
-- auth_header and auth_scheme hold the presentation that goes on the wire; the
-- protocol convention (x-api-key for anthropic_messages, bearer otherwise) is
-- only their default. Recording the operator's explicit choice in its own
-- column is what lets an update that omits `auth` preserve it, including when
-- that same update rewrites the protocol.
ALTER TABLE llm_provider ADD COLUMN auth TEXT NOT NULL DEFAULT '';

-- Backfill only rows whose stored presentation differs from their protocol
-- default: that difference is the sole evidence of an explicit override. Rows
-- that match the convention stay empty so a later protocol change still
-- refreshes their header instead of pinning the old one.
UPDATE llm_provider
SET auth = CASE
    WHEN auth_header = 'x-api-key' AND default_wire_api != 'anthropic_messages' THEN 'x-api-key'
    WHEN auth_header = 'Authorization' AND auth_scheme = 'Bearer' AND default_wire_api = 'anthropic_messages' THEN 'bearer'
    ELSE ''
END;
