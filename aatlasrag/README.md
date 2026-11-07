# AtlasRAG application

AtlasRAG is the event-driven backend in this repository. It accepts PDF
uploads through a Go API and processes them asynchronously with a Python
worker, Kafka, PostgreSQL and self-hosted MinIO.

For the architecture diagrams, document flow, local setup, API overview and
current limitations, start with the [repository README](../README.md).

## Components

- [Go API](apps/api-go/README.md): PDF upload, status, hybrid search, answer generation, embedded frontend and
  transactional outbox publisher.
- [Python worker](apps/workers-python/README.md): PDF extraction, chunking and
  optional OpenAI descriptions and embeddings.
- [Infrastructure](infra/README.md): local Compose services and MinIO build.
- [Migrations](migrations/README.md): PostgreSQL tables and pgvector schema.
- [Event contracts](contracts/events/README.md): `document.uploaded` v1.
- [Design notes](multimodal_README.md): broader architecture ideas; proposed
  components there are not necessarily implemented.

## Status

The ingestion pipeline, optional indexing and prototype hybrid retrieval are
in place, as is prototype GPT-4o answer generation. Live indexing and retrieval
have been exercised; answer generation and overall quality still need live
evaluation. Production deployment is future work. `COMPLETED` means the configured processing path finished;
indexing is disabled by default.
