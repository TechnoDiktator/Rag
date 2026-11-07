"""Consume document.uploaded events and persist extracted PDF page text."""

from __future__ import annotations

import hashlib
import json
import logging
import os
import signal
import sys
from dataclasses import dataclass
from datetime import datetime
from io import BytesIO
from typing import TYPE_CHECKING, Any
from uuid import UUID

import psycopg
from kafka import KafkaConsumer
from minio import Minio
from pypdf import PdfReader

if TYPE_CHECKING:
    from atlasrag_workers.unstructured_pdf import ExtractedElement

LOGGER = logging.getLogger("atlasrag.worker")
EVENT_TYPE = "document.uploaded"
EVENT_VERSION = 1


@dataclass(frozen=True)
class PageText:
    page_number: int
    text: str


class InvalidEventError(ValueError):
    """Raised when a Kafka message does not conform to event contract v1."""


class PDFParseError(ValueError):
    """Raised when the referenced object cannot be parsed as a PDF."""


class InvalidPDFObjectError(ValueError):
    """Raised when the stored object does not match the upload event."""


def validate_event(payload: Any) -> dict[str, Any]:
    if not isinstance(payload, dict):
        raise InvalidEventError("event must be a JSON object")
    required = {
        "event_id",
        "event_type",
        "event_version",
        "occurred_at",
        "correlation_id",
        "document_id",
        "document_version",
        "payload",
    }
    if not required.issubset(payload):
        raise InvalidEventError("event is missing required envelope fields")
    if set(payload) != required:
        raise InvalidEventError("event contains unsupported envelope fields")
    try:
        UUID(payload["event_id"])
        UUID(payload["correlation_id"])
        UUID(payload["document_id"])
        occurred_at = datetime.fromisoformat(payload["occurred_at"].replace("Z", "+00:00"))
    except (AttributeError, TypeError, ValueError) as exc:
        raise InvalidEventError("event contains an invalid UUID or timestamp") from exc
    if occurred_at.tzinfo is None or occurred_at.utcoffset() is None:
        raise InvalidEventError("occurred_at must include a timezone")
    if (
        payload["event_type"] != EVENT_TYPE
        or isinstance(payload["event_version"], bool)
        or payload["event_version"] != EVENT_VERSION
    ):
        raise InvalidEventError("unsupported event type or version")
    if (
        not isinstance(payload["document_version"], int)
        or isinstance(payload["document_version"], bool)
        or payload["document_version"] < 1
    ):
        raise InvalidEventError("document_version must be a positive integer")

    data = payload["payload"]
    payload_fields = {"filename", "object_key", "content_hash", "mime_type", "file_size_bytes"}
    if not isinstance(data, dict) or not payload_fields.issubset(data):
        raise InvalidEventError("event payload is missing required fields")
    if set(data) != payload_fields:
        raise InvalidEventError("event contains unsupported payload fields")
    if not isinstance(data["filename"], str) or not data["filename"]:
        raise InvalidEventError("filename must be a non-empty string")
    if not isinstance(data["object_key"], str) or not data["object_key"]:
        raise InvalidEventError("object_key must be a non-empty string")
    if data["mime_type"] != "application/pdf":
        raise InvalidEventError("only application/pdf events are supported")
    content_hash = data["content_hash"]
    if (
        not isinstance(content_hash, str)
        or len(content_hash) != 64
        or any(char not in "0123456789abcdef" for char in content_hash)
    ):
        raise InvalidEventError("content_hash must be a SHA-256 hex digest")
    size = data["file_size_bytes"]
    if not isinstance(size, int) or isinstance(size, bool) or size < 0:
        raise InvalidEventError("file_size_bytes must be a non-negative integer")
    return payload


def parse_pdf(content: bytes) -> list[PageText]:
    try:
        reader = PdfReader(BytesIO(content), strict=True)
        if reader.is_encrypted:
            raise PDFParseError("encrypted PDFs are not supported yet")
        pages = [
            PageText(page_number=index, text=page.extract_text() or "")
            for index, page in enumerate(reader.pages, start=1)
        ]
    except PDFParseError:
        raise
    except Exception as exc:
        raise PDFParseError("could not parse PDF content") from exc
    if not pages:
        raise PDFParseError("PDF contains no pages")
    return pages


class DocumentWorker:
    def __init__(
        self,
        database_url: str,
        bucket: str,
        object_client: Minio,
        max_pdf_bytes: int = 50 << 20,
        pdf_extractor: str = "pypdf",
    ) -> None:
        if pdf_extractor not in {"pypdf", "unstructured"}:
            raise ValueError("PDF_EXTRACTOR must be pypdf or unstructured")
        self.database_url = database_url
        self.bucket = bucket
        self.object_client = object_client
        self.max_pdf_bytes = max_pdf_bytes
        self.pdf_extractor = pdf_extractor

    def process(self, raw_event: bytes, message_key: bytes | None = None) -> None:
        try:
            event = validate_event(json.loads(raw_event))
        except (json.JSONDecodeError, UnicodeDecodeError, InvalidEventError) as exc:
            raise InvalidEventError("invalid document.uploaded message") from exc
        if message_key and message_key.decode("utf-8") != event["document_id"]:
            raise InvalidEventError("Kafka key does not match document_id")

        document_id = event["document_id"]
        version = event["document_version"]
        object_key = event["payload"]["object_key"]

        self._mark_running(document_id, version)
        try:
            pdf_content = self._download_pdf(object_key)
            if len(pdf_content) != event["payload"]["file_size_bytes"]:
                raise InvalidPDFObjectError("object size does not match the event")
            actual_hash = hashlib.sha256(pdf_content).hexdigest()
            if actual_hash != event["payload"]["content_hash"]:
                raise InvalidPDFObjectError("object SHA-256 does not match the event")
        except InvalidPDFObjectError as exc:
            self._mark_failed(document_id, version, str(exc))
            LOGGER.warning("document %s v%s has an invalid source object: %s", document_id, version, exc)
            return

        try:
            pages = parse_pdf(pdf_content)
        except PDFParseError as exc:
            self._mark_failed(document_id, version, str(exc))
            LOGGER.warning("document %s v%s failed parsing: %s", document_id, version, exc)
            return

        elements = None
        if self.pdf_extractor == "unstructured":
            # Import only when enabled, so the existing text-only mode stays usable.
            from atlasrag_workers.unstructured_pdf import extract_elements

            elements = extract_elements(
                pdf_content, document_id, version, len(pages), self.object_client, self.bucket
            )
            if not elements:
                reason = "Unstructured returned no elements; the PDF may be blank or extraction failed"
                self._mark_failed(document_id, version, reason)
                LOGGER.warning("document %s v%s failed parsing: %s", document_id, version, reason)
                return
            text_by_page: dict[int, list[str]] = {page.page_number: [] for page in pages}
            for element in elements:
                text = element.content.get("text")
                if isinstance(text, str) and text:
                    text_by_page[element.page_number].append(text)
            pages = [
                PageText(page_number, "\n\n".join(text_by_page[page_number]))
                for page_number in text_by_page
            ]

        self._save_pages_and_complete(document_id, version, pages, elements)
        LOGGER.info("document %s v%s parsed: %d pages", document_id, version, len(pages))

    def _download_pdf(self, object_key: str) -> bytes:
        response = self.object_client.get_object(self.bucket, object_key)
        content = BytesIO()
        try:
            while True:
                chunk = response.read(64 * 1024)
                if not chunk:
                    break
                content.write(chunk)
                if content.tell() > self.max_pdf_bytes:
                    raise InvalidPDFObjectError("PDF exceeds the configured worker size limit")
        finally:
            response.close()
            response.release_conn()
        return content.getvalue()

    def _mark_running(self, document_id: str, version: int) -> None:
        with psycopg.connect(self.database_url) as connection:
            with connection.cursor() as cursor:
                cursor.execute(
                    """
                    UPDATE ingestion_jobs
                    SET status = 'RUNNING', started_at = COALESCE(started_at, now()),
                        error_message = NULL
                    WHERE document_id = %s AND version = %s AND stage = 'PARSE'
                    """,
                    (document_id, version),
                )
                if cursor.rowcount != 1:
                    raise RuntimeError("parse job does not exist for document version")
                cursor.execute(
                    "UPDATE documents SET status = 'PARSING', updated_at = now() WHERE id = %s",
                    (document_id,),
                )
                cursor.execute(
                    "UPDATE document_versions SET status = 'PARSING' WHERE document_id = %s AND version = %s",
                    (document_id, version),
                )

    def _mark_failed(self, document_id: str, version: int, reason: str) -> None:
        with psycopg.connect(self.database_url) as connection:
            with connection.cursor() as cursor:
                cursor.execute(
                    """
                    UPDATE ingestion_jobs
                    SET status = 'FAILED', error_message = %s, completed_at = now()
                    WHERE document_id = %s AND version = %s AND stage = 'PARSE'
                    """,
                    (reason[:2000], document_id, version),
                )
                cursor.execute(
                    "UPDATE document_versions SET status = 'FAILED' WHERE document_id = %s AND version = %s",
                    (document_id, version),
                )
                cursor.execute(
                    "UPDATE documents SET status = 'FAILED', updated_at = now() WHERE id = %s",
                    (document_id,),
                )

    def _save_pages_and_complete(
        self, document_id: str, version: int, pages: list[PageText],
        elements: list[ExtractedElement] | None = None,
    ) -> None:
        with psycopg.connect(self.database_url) as connection:
            with connection.cursor() as cursor:
                cursor.execute(
                    "DELETE FROM document_pages WHERE document_id = %s AND version = %s",
                    (document_id, version),
                )
                cursor.executemany(
                    """
                    INSERT INTO document_pages (
                        document_id, version, page_number, extracted_text, extraction_method
                    ) VALUES (%s, %s, %s, %s, %s)
                    ON CONFLICT (document_id, version, page_number)
                    DO UPDATE SET extracted_text = EXCLUDED.extracted_text,
                                  extraction_method = EXCLUDED.extraction_method
                    """,
                    [
                        (document_id, version, page.page_number, page.text, self.pdf_extractor)
                        for page in pages
                    ],
                )
                if elements is not None:
                    cursor.execute(
                        "DELETE FROM document_elements WHERE document_id = %s AND version = %s",
                        (document_id, version),
                    )
                    cursor.executemany(
                        """
                        INSERT INTO document_elements
                            (document_id, version, element_index, page_number,
                             category, content, image_object_key)
                        VALUES (%s, %s, %s, %s, %s, %s::jsonb, %s)
                        """,
                        [
                            (document_id, version, index, element.page_number,
                             element.category, json.dumps(element.content), element.image_object_key)
                            for index, element in enumerate(elements)
                        ],
                    )
                cursor.execute(
                    """
                    UPDATE ingestion_jobs
                    SET status = 'COMPLETED', completed_at = now(), error_message = NULL
                    WHERE document_id = %s AND version = %s AND stage = 'PARSE'
                    """,
                    (document_id, version),
                )
                cursor.execute(
                    """
                    UPDATE document_versions SET status = 'COMPLETED'
                    WHERE document_id = %s AND version = %s
                    """,
                    (document_id, version),
                )
                cursor.execute(
                    """
                    UPDATE documents
                    SET status = 'COMPLETED', page_count = %s, updated_at = now()
                    WHERE id = %s AND current_version = %s
                    """,
                    (len(pages), document_id, version),
                )


def required_env(name: str) -> str:
    value = os.getenv(name)
    if not value:
        raise RuntimeError(f"{name} is required")
    return value


def main() -> None:
    logging.basicConfig(
        level=os.getenv("LOG_LEVEL", "INFO").upper(),
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
    )
    max_pdf_bytes = int(os.getenv("MAX_PDF_BYTES", str(50 << 20)))
    if max_pdf_bytes <= 0:
        raise RuntimeError("MAX_PDF_BYTES must be positive")

    database_url = required_env("DATABASE_URL")
    bucket = required_env("S3_BUCKET")
    ssl_value = os.getenv("S3_USE_SSL", "false").lower()
    if ssl_value not in {"1", "true", "yes", "0", "false", "no"}:
        raise RuntimeError("S3_USE_SSL must be a boolean")
    secure = ssl_value in {"1", "true", "yes"}
    object_client = Minio(
        required_env("S3_ENDPOINT"),
        access_key=required_env("S3_ACCESS_KEY"),
        secret_key=required_env("S3_SECRET_KEY"),
        secure=secure,
    )
    consumer = KafkaConsumer(
        os.getenv("KAFKA_TOPIC", EVENT_TYPE),
        bootstrap_servers=required_env("KAFKA_BOOTSTRAP_SERVERS").split(","),
        group_id=os.getenv("KAFKA_GROUP_ID", "atlasrag-parser"),
        enable_auto_commit=False,
        auto_offset_reset="earliest",
        value_deserializer=None,
        max_poll_interval_ms=int(os.getenv("KAFKA_MAX_POLL_INTERVAL_MS", "1800000")),
    )
    worker = DocumentWorker(
        database_url, bucket, object_client, max_pdf_bytes,
        os.getenv("PDF_EXTRACTOR", "pypdf"),
    )

    stopping = False

    def stop_worker(_signum: int, _frame: Any) -> None:
        nonlocal stopping
        stopping = True

    signal.signal(signal.SIGTERM, stop_worker)
    signal.signal(signal.SIGINT, stop_worker)
    LOGGER.info("waiting for %s events", os.getenv("KAFKA_TOPIC", EVENT_TYPE))
    try:
        while not stopping:
            records = consumer.poll(timeout_ms=1000, max_records=1)
            for messages in records.values():
                for message in messages:
                    worker.process(message.value, message.key)
                    consumer.commit()
    except Exception:
        LOGGER.exception("worker stopped without acknowledging the failed message")
        sys.exit(1)
    finally:
        consumer.close()


if __name__ == "__main__":
    main()
