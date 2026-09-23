-- The exact model reference the guest was told to use.
--
-- Facade tokens are connection-bound, so the upstream model is already known
-- (model) and the guest addresses it in its own namespace (pi and opencode use
-- <connection>/<model>). Recording the guest-facing spelling lets the proxy map
-- an exact match back to the literal upstream model without parsing the string,
-- which is what keeps a model id that itself contains a slash intact.
ALTER TABLE llm_facade_token ADD COLUMN guest_model TEXT NOT NULL DEFAULT '';
