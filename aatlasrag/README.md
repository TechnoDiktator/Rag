# AtlasRAG

Distributed multimodal document intelligence platform. The Go API handles
client-facing requests; Python workers process documents asynchronously.

## Repository layout

- `apps/api-go` — Go HTTP API.
- `apps/workers-python` — Python background processing workers.
- `contracts/events` — schemas shared by services through Kafka.
- `migrations` — PostgreSQL schema migrations.
- `infra` — local development and deployment configuration.
- `docs/adr` — architecture decision records.

## Build order

1. Define document lifecycle tables and event contracts.
2. Add upload and object storage support to the Go API.
3. Process uploaded PDFs in a Python worker.
4. Add chunking, embeddings, and vector search.

Start with one working path from PDF upload through processing before adding
more services or infrastructure.
