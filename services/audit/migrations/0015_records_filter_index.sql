CREATE INDEX IF NOT EXISTS audit_entry_user ON audit_entry (tenant_id, user_id, seq);
