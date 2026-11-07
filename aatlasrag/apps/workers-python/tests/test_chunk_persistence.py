import os
import unittest
from contextlib import contextmanager
from unittest.mock import Mock, patch
from uuid import uuid4

import psycopg

from atlasrag_workers.chunking import ChunkSource, DocumentChunk
from atlasrag_workers.consumer import DocumentWorker, PageText
from atlasrag_workers.indexing import EMBEDDING_DIMENSIONS, IndexedChunk
from atlasrag_workers.unstructured_pdf import ExtractedElement


@unittest.skipUnless(os.getenv("ATLASRAG_TEST_DB_HOST"), "integration database not configured")
class ChunkPersistenceTests(unittest.TestCase):
    def test_writes_sources_and_rolls_back_failed_replacement(self):
        connection = psycopg.connect(
            host=os.environ["ATLASRAG_TEST_DB_HOST"],
            port=os.environ.get("ATLASRAG_TEST_DB_PORT", "5433"),
            user=os.environ["ATLASRAG_TEST_DB_USER"],
            password=os.environ["ATLASRAG_TEST_DB_PASSWORD"],
            dbname=os.environ["ATLASRAG_TEST_DB_NAME"],
        )
        self.addCleanup(connection.close)

        @contextmanager
        def db_context(_url):
            with connection.transaction():
                yield connection

        document_id = str(uuid4())
        content_hash = uuid4().hex * 2
        worker = DocumentWorker("unused", "bucket", Mock(), pdf_extractor="unstructured")
        page = PageText(1, "hello")
        element = ExtractedElement(1, "Image", {"type": "Image", "metadata": {"page_number": 1}}, "image-key")
        chunk = DocumentChunk("hello", [ChunkSource(1, 0)])

        with connection.transaction(force_rollback=True):
            connection.execute(
                """INSERT INTO documents (id, filename, object_key, content_hash, mime_type, file_size_bytes)
                   VALUES (%s, 'test.pdf', 'test-key', %s, 'application/pdf', 1)""",
                (document_id, content_hash),
            )
            connection.execute(
                """INSERT INTO document_versions (document_id, version, object_key, content_hash)
                   VALUES (%s, 1, 'test-key', %s)""",
                (document_id, content_hash),
            )
            connection.execute(
                "INSERT INTO ingestion_jobs (document_id, version, stage) VALUES (%s, 1, 'PARSE')",
                (document_id,),
            )
            with patch("atlasrag_workers.consumer.psycopg.connect", side_effect=db_context):
                worker._save_pages_and_complete(document_id, 1, [page], [element], [chunk])
                row = connection.execute(
                    """SELECT c.content, s.page_number, e.image_object_key
                       FROM document_chunks c
                       JOIN document_chunk_sources s USING (document_id, version, chunk_index)
                       JOIN document_elements e USING (document_id, version, element_index)
                       WHERE c.document_id = %s""",
                    (document_id,),
                ).fetchone()
                self.assertEqual(row, ("hello", 1, "image-key"))

                invalid_chunk = DocumentChunk("bad", [ChunkSource(1, 999)])
                with self.assertRaises(psycopg.errors.ForeignKeyViolation):
                    worker._save_pages_and_complete(document_id, 1, [page], [element], [invalid_chunk])
                self.assertEqual(
                    connection.execute(
                        "SELECT content FROM document_chunks WHERE document_id = %s", (document_id,)
                    ).fetchone(),
                    ("hello",),
                )

                worker.pdf_extractor = "pypdf"
                worker._save_pages_and_complete(
                    document_id, 1, [page], None, [DocumentChunk("hello", [ChunkSource(1, None)])]
                )
                self.assertEqual(
                    connection.execute(
                        "SELECT count(*) FROM document_elements WHERE document_id = %s", (document_id,)
                    ).fetchone(),
                    (0,),
                )

    @unittest.skipUnless(os.getenv("ATLASRAG_TEST_PGVECTOR"), "pgvector test database not configured")
    def test_saves_embedding_and_rolls_back_invalid_vector(self):
        connection = psycopg.connect(
            host=os.environ["ATLASRAG_TEST_DB_HOST"],
            port=os.environ.get("ATLASRAG_TEST_DB_PORT", "5433"),
            user=os.environ["ATLASRAG_TEST_DB_USER"],
            password=os.environ["ATLASRAG_TEST_DB_PASSWORD"],
            dbname=os.environ["ATLASRAG_TEST_DB_NAME"],
        )
        self.addCleanup(connection.close)

        @contextmanager
        def db_context(_url):
            with connection.transaction():
                yield connection

        document_id = str(uuid4())
        content_hash = uuid4().hex * 2
        worker = DocumentWorker("unused", "bucket", Mock())
        with connection.transaction(force_rollback=True):
            connection.execute(
                """INSERT INTO documents (id, filename, object_key, content_hash, mime_type, file_size_bytes)
                   VALUES (%s, 'test.pdf', 'test-key', %s, 'application/pdf', 1)""",
                (document_id, content_hash),
            )
            connection.execute(
                """INSERT INTO document_versions (document_id, version, object_key, content_hash)
                   VALUES (%s, 1, 'test-key', %s)""",
                (document_id, content_hash),
            )
            connection.execute(
                "INSERT INTO ingestion_jobs (document_id, version, stage) VALUES (%s, 1, 'PARSE')",
                (document_id,),
            )
            chunks = [DocumentChunk("hello", [ChunkSource(1, None)])]
            with patch("atlasrag_workers.consumer.psycopg.connect", side_effect=db_context):
                worker._save_pages_and_complete(
                    document_id, 1, [PageText(1, "hello")], None, chunks,
                    [IndexedChunk(None, "hello", [0.1] * EMBEDDING_DIMENSIONS)],
                )
                row = connection.execute(
                    """SELECT embedding_model, searchable_text, vector_dims(embedding)
                       FROM document_chunk_embeddings WHERE document_id = %s""",
                    (document_id,),
                ).fetchone()
                self.assertEqual(row, ("text-embedding-3-small", "hello", EMBEDDING_DIMENSIONS))
                distance = connection.execute(
                    """SELECT embedding <=> %s::vector
                       FROM document_chunk_embeddings WHERE document_id = %s""",
                    ("[" + ",".join(["0.1"] * EMBEDDING_DIMENSIONS) + "]", document_id),
                ).fetchone()[0]
                self.assertAlmostEqual(distance, 0.0, places=5)
                with self.assertRaises(psycopg.errors.DataException):
                    worker._save_pages_and_complete(
                        document_id, 1, [PageText(1, "hello")], None, chunks,
                        [IndexedChunk(None, "bad", [0.1])],
                    )
                self.assertEqual(
                    connection.execute(
                        "SELECT searchable_text FROM document_chunk_embeddings WHERE document_id = %s",
                        (document_id,),
                    ).fetchone(),
                    ("hello",),
                )
