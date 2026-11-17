# eventing

At-least-once CloudEvents consumer. A handler error is retried with exponential backoff
(`ExponentialBackoff`: 2 s doubling to 2 min, `DefaultMaxAttempts` 10, about 8 minutes in all), so a
short database outage does not dead-letter events. Both are set per consumer with `Config.MaxAttempts`
and `Config.Backoff`. A `Permanent` error, an undecodable record or an exhausted retry goes to
`Config.DeadLetterTopic` and the offset moves on.

Replay: fix the cause, then re-publish the dead-lettered records from `<topic>.dlq` to the source topic
(the original topic, partition and offset are in the `x-origin-*` headers, the reason in `x-dlq-reason`).
Handlers are idempotent by event id and their own keys, so a replay is safe. Alert on
`event_consumer_dead_lettered_total` and `event_consumer_dead_letter_failures_total`.

Records that are not CloudEvents: set `Config.Decode` to turn each record into the event the handlers see, with a stable
ID (the inbox key) and a tenant; its error dead-letters the record as an undecodable one. The audit CDC consumer reads
Debezium change events this way (`services/audit/internal/cdc`).
