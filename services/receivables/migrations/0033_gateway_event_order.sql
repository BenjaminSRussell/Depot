-- #30 review: the provider's time of the latest event applied to a portal payment attempt, so an older event that arrives late
-- (Stripe does not order deliveries) never moves the attempt back, for example a stale payment_failed after a newer processing.
-- And the indexes the reservation check reads: attempts still waiting for the customer or processing, by item.
ALTER TABLE gateway_payment ADD COLUMN IF NOT EXISTS last_event_at timestamptz;
CREATE INDEX IF NOT EXISTS gateway_payment_pending ON gateway_payment (tenant_id, status, created_at)
  WHERE status IN ('REQUIRES_PAYMENT', 'PROCESSING');
CREATE INDEX IF NOT EXISTS gateway_payment_allocation_item ON gateway_payment_allocation (tenant_id, open_item_id);

-- #30 review: the lease a webhook handling holds on its attempt, instead of a session-level advisory lock (the production pooler
-- runs transaction mode, docs/architecture.md). Taken with a conditional UPDATE, released by its token, and it expires on its
-- own after two minutes, so a crashed handler never blocks the attempt and no connection is held while it is taken.
ALTER TABLE gateway_payment ADD COLUMN IF NOT EXISTS handling_until timestamptz;
ALTER TABLE gateway_payment ADD COLUMN IF NOT EXISTS handling_token uuid;
