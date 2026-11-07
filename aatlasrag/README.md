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

1. **Foundation in place:** document lifecycle migration and the
   `document.uploaded` v1 Kafka contract.
2. **Upload API in place:** accept PDFs, store them in S3-compatible object
   storage, and transactionally create the document, version, parse job, and
   outbox event. See `apps/api-go/README.md` for configuration.
3. **Async parse path in place:** publish outbox events to Kafka, parse PDFs
   with the Python worker, and query document status.
4. Add chunking, embeddings, and vector search.

PostgreSQL migrations are in `migrations/`. The upload event schema and
example are in `contracts/events/`.

Start with one working path from PDF upload through processing before adding
more services or infrastructure.
