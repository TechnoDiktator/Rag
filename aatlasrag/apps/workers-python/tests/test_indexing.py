import unittest
from types import SimpleNamespace
from unittest.mock import Mock

from atlasrag_workers.chunking import ChunkSource, DocumentChunk
from atlasrag_workers.indexing import EMBEDDING_DIMENSIONS, OpenAIIndexer
from atlasrag_workers.unstructured_pdf import ExtractedElement


class IndexingTests(unittest.TestCase):
    def setUp(self):
        self.client = Mock()
        self.store = Mock()
        self.client.embeddings.create.return_value = SimpleNamespace(
            data=[SimpleNamespace(embedding=[0.1] * EMBEDDING_DIMENSIONS)]
        )
        self.client.chat.completions.create.return_value = SimpleNamespace(
            choices=[SimpleNamespace(finish_reason="stop", message=SimpleNamespace(content="A chart"))]
        )
        self.indexer = OpenAIIndexer(self.client, self.store, "bucket")

    def test_text_only_does_not_invoke_vision(self):
        result = self.indexer.index([DocumentChunk("Hello", [ChunkSource(1, None)])], None)
        self.assertEqual(result[0].searchable_text, "Hello")
        self.assertIsNone(result[0].description)
        self.client.chat.completions.create.assert_not_called()
        self.client.embeddings.create.assert_called_once()

    def test_image_only_description_is_embedded(self):
        response = Mock()
        response.read.return_value = b"fake image"
        self.store.get_object.return_value = response
        image = ExtractedElement(
            1, "Image", {"metadata": {"image_mime_type": "image/png"}}, "image-key"
        )
        result = self.indexer.index([DocumentChunk("", [ChunkSource(1, 0)])], [image])
        self.assertEqual(result[0].searchable_text, "A chart")
        message = self.client.chat.completions.create.call_args.kwargs["messages"][0]
        self.assertEqual(message["content"][1]["type"], "image_url")
        self.assertTrue(message["content"][1]["image_url"]["url"].startswith("data:image/png;base64,"))
        response.close.assert_called_once()
        response.release_conn.assert_called_once()
        self.assertEqual(self.client.embeddings.create.call_args.kwargs["input"], "A chart")

    def test_table_html_is_sent_to_vision(self):
        table = ExtractedElement(1, "Table", {"metadata": {"text_as_html": "<table>123</table>"}}, None)
        result = self.indexer.index([DocumentChunk("Table text", [ChunkSource(1, 0)])], [table])
        message = self.client.chat.completions.create.call_args.kwargs["messages"][0]
        self.assertIn("<table>123</table>", message["content"][0]["text"])
        self.assertEqual(result[0].searchable_text, "Table text\n\nA chart")

    def test_rejects_bad_embedding_and_incomplete_vision_response(self):
        self.client.embeddings.create.return_value.data[0].embedding = [1.0]
        with self.assertRaisesRegex(ValueError, "dimensions"):
            self.indexer.index([DocumentChunk("text", [ChunkSource(1, None)])], None)
        self.client.chat.completions.create.return_value.choices[0].finish_reason = "length"
        table = ExtractedElement(1, "Table", {"metadata": {"text_as_html": "<table/>"}}, None)
        with self.assertRaisesRegex(ValueError, "complete description"):
            self.indexer.index([DocumentChunk("text", [ChunkSource(1, 0)])], [table])

    def test_vision_failure_propagates_without_embedding(self):
        self.client.chat.completions.create.side_effect = RuntimeError("unavailable")
        table = ExtractedElement(1, "Table", {"metadata": {"text_as_html": "<table/>"}}, None)
        with self.assertRaisesRegex(RuntimeError, "unavailable"):
            self.indexer.index([DocumentChunk("text", [ChunkSource(1, 0)])], [table])
        self.client.embeddings.create.assert_not_called()
