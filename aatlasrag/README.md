# AtlasRAG application

AtlasRAG is the event-driven backend in this repository. It accepts PDF
uploads through a Go API and processes them asynchronously with a Python
worker, Kafka, PostgreSQL and self-hosted MinIO.

For the architecture diagrams, document flow, local setup, API overview and
current limitations, start with the [repository README](../README.md).

## Components

- [Go API](apps/api-go/README.md): PDF upload, status, embedded frontend and
  transactional outbox publisher.
- [Python worker](apps/workers-python/README.md): PDF extraction, chunking and
  optional OpenAI descriptions and embeddings.
- [Infrastructure](infra/README.md): local Compose services and MinIO build.
- [Migrations](migrations/README.md): PostgreSQL tables and pgvector schema.
- [Event contracts](contracts/events/README.md): `document.uploaded` v1.
- [Design notes](multimodal_README.md): broader architecture ideas; proposed
  components there are not necessarily implemented.

## Status

The ingestion pipeline and optional indexing code are in place. The optional
OpenAI path has mocked API and isolated pgvector tests, but has not been
verified end to end in the worker container with live OpenAI calls. Retrieval,
answer generation and deployment are future work. `COMPLETED` means the
configured processing path finished; indexing is disabled by default.
