# Event contracts

Store versioned JSON Schemas and example messages exchanged through Kafka
here. Keep contracts independent of Go and Python implementation details.

## `document.uploaded` v1

- Schema: `document.uploaded.v1.schema.json`
- Example: `document.uploaded.v1.example.json`
- Kafka key: `document_id`
- The event is written to the PostgreSQL outbox in the same transaction as
  document registration. It contains an object-storage key, never PDF bytes.
- `event_id` is the outbox row ID and is the deduplication key for consumers.
- `correlation_id` should be retained by downstream events for tracing.

For incompatible changes, publish a new event version and schema rather than
silently changing the meaning of v1.
