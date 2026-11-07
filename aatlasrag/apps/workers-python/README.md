# Python PDF worker

The worker consumes `document.uploaded` v1 messages from Kafka, downloads the
referenced object from MinIO, extracts per-page text with pypdf, and stores page
text and parse status in PostgreSQL.

Compose runs this worker with internal service addresses (`postgres:5432`,
`minio:9000`, and `kafka:29092`). Run `docker compose logs -f python-worker`
from `infra/` to follow its output.

Kafka offsets are committed only after the worker finishes a message. The
database writes are idempotent for page/document/version keys, so a message
redelivered after a crash can be safely processed again. Infrastructure errors
are not acknowledged and cause the worker to exit so Compose can restart it.
Malformed or unsupported events also stop the worker rather than being silently
discarded; a dead-letter queue and bounded retry policy are follow-up work.

The default pypdf mode extracts text from digitally generated PDFs; it does not
perform OCR or extract image and table structure.

## Optional Unstructured extraction

Set `PDF_EXTRACTOR=unstructured` in `infra/.env` and rebuild the worker to use
the notebook's `hi_res` PDF extraction path. The default is `pypdf`; changing
the setting does not reprocess existing documents. Apply migration 000003
before enabling the new mode. Extracted elements, page numbers, and table HTML
are stored in PostgreSQL; image payloads are stored in MinIO with object keys
in `document_elements`. Element and page writes commit together. Image keys
include a content hash, so retries do not overwrite different images. Failures
or reprocessing can leave unreferenced objects; cleanup is not implemented.

The worker image installs Poppler, Tesseract (English), and libmagic. The
layout model may need to download weights on the first run. This extraction
stage does not yet summarize images or tables, build embeddings, or produce
searchable answers. OCR and table accuracy depend on the input; test on your
own documents before relying on the output.

An empty Unstructured result marks the parse as failed, including for a blank
PDF. Other extraction or infrastructure errors stop the worker without
acknowledging the message and may cause repeated retries. The Kafka poll
interval defaults to 30 minutes (`KAFKA_MAX_POLL_INTERVAL_MS`); an extraction
taking longer may be redelivered. This is a local-development worker, not a
production job scheduling or retry solution.

Run the unit tests from this directory after installing the project:

```powershell
python -m pip install -e .
python -m unittest discover -s tests
```
