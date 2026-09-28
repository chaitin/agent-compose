-- Trusted ingress headers (x-mpi-*) of the most recent ApplyProject request.
--
-- Cron and event scheduler runs have no request of their own, so they carry
-- the identity of whoever last applied the Project. The value is a JSON array
-- of {"name","value"} objects in request order; '[]' means the last apply
-- carried no trusted headers, which is also the state of Projects applied
-- before this column existed.
ALTER TABLE project ADD COLUMN apply_trusted_headers_json TEXT NOT NULL DEFAULT '[]';
