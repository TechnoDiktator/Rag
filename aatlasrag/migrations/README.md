# Database migrations

These are PostgreSQL migrations. `000001_create_document_lifecycle.up.sql`
creates the initial document, version, ingestion-job, and transactional
outbox tables. `000002_create_document_pages.up.sql` adds the per-page text
table used by the PDF parser. `000003_create_document_elements.up.sql` adds
structured PDF elements and image object references. Matching `.down.sql`
files remove each migration's objects.

On a brand-new Compose database volume, the `.up.sql` files are applied
automatically. On an existing volume, initialization scripts do not run again.
For an existing volume with 000001 and 000002 already applied, apply 000003
once from `infra/` without deleting data:

```powershell
Get-Content ..\migrations\000003_create_document_elements.up.sql -Raw |
  docker exec -i atlasrag-postgres psql -v ON_ERROR_STOP=1 -U atlasrag -d atlasrag
```

If 000002 is not present yet, apply it first using the same command with its
filename. Check which tables exist before applying a migration; these SQL
scripts are not migration-tracked or repeatable.

For later schema changes, add a new numbered migration and use a migration
runner rather than relying on container initialization.

The upload transaction should create the `documents` row, its initial
`document_versions` row, a `PARSE` ingestion job, and a complete
`document.uploaded` envelope in `outbox_events`. The publisher sends the
outbox payload to Kafka using `aggregate_id` as the message key, then sets
`published_at`. Consumers must tolerate duplicate delivery.

Keep document lifecycle state in PostgreSQL as the source of truth. Add a new
numbered migration for subsequent schema changes; do not edit an applied
migration.
