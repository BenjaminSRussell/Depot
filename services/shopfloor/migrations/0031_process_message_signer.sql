-- #25: who made a signature and when they authenticated. The service sets both from the caller's identity (the user id
-- and the token's auth_time, 21 CFR 11.200 step-up); the client cannot. Null for messages that carry no signature.
ALTER TABLE process_message ADD COLUMN IF NOT EXISTS signer_user text CHECK (signer_user IS NULL OR char_length(signer_user) BETWEEN 1 AND 200);
ALTER TABLE process_message ADD COLUMN IF NOT EXISTS signer_auth_time timestamptz;
