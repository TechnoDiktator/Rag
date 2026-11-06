# Go API

## Upload a PDF

Run the API with these environment variables set:

- `DATABASE_URL` — PostgreSQL connection URL. Apply the root `migrations/`
  before starting the API.
- `S3_ENDPOINT` — S3-compatible endpoint, such as `localhost:9000`.
- `S3_ACCESS_KEY` and `S3_SECRET_KEY` — object-storage credentials.
- `S3_BUCKET` — an existing bucket for PDF objects.
- `S3_USE_SSL` — optional boolean, defaults to `false`.
- `MAX_UPLOAD_BYTES` — optional maximum PDF size in bytes, defaults to 50 MiB.
- `HTTP_ADDR` — optional listen address, defaults to `:8080`.

The API checks PostgreSQL connectivity and that the configured bucket exists
when it starts. For local PostgreSQL and MinIO setup, see `infra/README.md`;
Compose applies the migration to a new database volume and creates the bucket.

Send one multipart file using the `file` field:

```text
POST /api/v1/documents
Content-Type: multipart/form-data
```

An optional `X-Correlation-ID` header must be a UUID. If omitted, the API
generates one. Successful uploads return `202 Accepted` with a `document_id`
and `QUEUED` status.

The API verifies the PDF signature, computes SHA-256, and stores the object
under a generated key. It then inserts the document, version, parse job, and
`document.uploaded` v1 outbox event in one PostgreSQL transaction. If database
registration fails, it attempts to delete the uploaded object. Identical
content in the shared (non-tenant) namespace returns `409 Conflict`.
If PostgreSQL reports an ambiguous commit result, the API retains the object
because the document transaction may have committed successfully.

This initial endpoint has no authentication or tenant assignment; add those
before exposing it to untrusted clients.
