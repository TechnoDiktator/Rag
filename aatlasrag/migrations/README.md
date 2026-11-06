# Database migrations

These are PostgreSQL migrations. Apply `000001_create_document_lifecycle.up.sql`
to create the initial document, version, ingestion-job, and transactional
outbox tables. The matching `.down.sql` removes those tables.

The upload transaction should create the `documents` row, its initial
`document_versions` row, a `PARSE` ingestion job, and a complete
`document.uploaded` envelope in `outbox_events`. The publisher sends the
outbox payload to Kafka using `aggregate_id` as the message key, then sets
`published_at`. Consumers must tolerate duplicate delivery.

Keep document lifecycle state in PostgreSQL as the source of truth. Add a new
numbered migration for subsequent schema changes; do not edit an applied
migration.
