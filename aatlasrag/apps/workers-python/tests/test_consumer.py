import hashlib
import json
import unittest
from io import BytesIO
from unittest.mock import Mock, patch

from pypdf import PdfWriter

from atlasrag_workers.consumer import DocumentWorker, InvalidEventError, parse_pdf, validate_event


class EventContractTests(unittest.TestCase):
    def valid_event(self):
        return {
            "event_id": "ec45a0cf-799a-4847-b424-17b7001bb5a0",
            "event_type": "document.uploaded",
            "event_version": 1,
            "occurred_at": "2026-11-06T18:20:00Z",
            "correlation_id": "f83d73c0-421c-4c4a-9df5-b69c7d687feb",
            "document_id": "99591d99-c13e-42d4-8e38-a44ba7a13cc7",
            "document_version": 1,
            "payload": {
                "filename": "annual-report.pdf",
                "object_key": "documents/example/v1/source.pdf",
                "content_hash": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
                "mime_type": "application/pdf",
                "file_size_bytes": 100,
            },
        }

    def test_accepts_valid_v1_event(self):
        event = self.valid_event()
        self.assertEqual(validate_event(event), event)

    def test_rejects_unsupported_event_type(self):
        event = self.valid_event()
        event["event_type"] = "document.changed"
        with self.assertRaises(InvalidEventError):
            validate_event(event)

    def test_rejects_invalid_hash(self):
        event = self.valid_event()
        event["payload"]["content_hash"] = "invalid"
        with self.assertRaises(InvalidEventError):
            validate_event(event)


class PDFParsingTests(unittest.TestCase):
    def test_parses_pdf_page_count(self):
        writer = PdfWriter()
        writer.add_blank_page(width=72, height=72)
        writer.add_blank_page(width=72, height=72)
        pdf = BytesIO()
        writer.write(pdf)

        pages = parse_pdf(pdf.getvalue())

        self.assertEqual([page.page_number for page in pages], [1, 2])
        self.assertEqual([page.text for page in pages], ["", ""])

    def test_rejects_invalid_pdf(self):
        with self.assertRaises(ValueError):
            parse_pdf(b"not a PDF")


class WorkerExtractionTests(unittest.TestCase):
    def setUp(self):
        writer = PdfWriter()
        writer.add_blank_page(width=72, height=72)
        pdf = BytesIO()
        writer.write(pdf)
        self.content = pdf.getvalue()
        self.event = EventContractTests().valid_event()
        self.event["payload"]["content_hash"] = hashlib.sha256(self.content).hexdigest()
        self.event["payload"]["file_size_bytes"] = len(self.content)
        self.worker = DocumentWorker("db", "bucket", Mock())
        self.worker._mark_running = Mock()
        self.worker._mark_failed = Mock()
        self.worker._download_pdf = Mock(return_value=self.content)
        self.worker._save_pages_and_complete = Mock()

    def test_pypdf_is_default(self):
        self.worker.process(json.dumps(self.event).encode())
        pages = self.worker._save_pages_and_complete.call_args.args[2]
        self.assertEqual(len(pages), 1)
        self.assertIsNone(self.worker._save_pages_and_complete.call_args.args[3])

    def test_empty_unstructured_result_marks_failed(self):
        self.worker.pdf_extractor = "unstructured"
        with patch("atlasrag_workers.unstructured_pdf.extract_elements", return_value=[]):
            self.worker.process(json.dumps(self.event).encode())
        self.worker._mark_failed.assert_called_once()
        self.worker._save_pages_and_complete.assert_not_called()

    def test_unstructured_text_is_saved_as_page_text(self):
        from atlasrag_workers.unstructured_pdf import ExtractedElement

        self.worker.pdf_extractor = "unstructured"
        elements = [
            ExtractedElement(1, "Title", {"type": "Title", "text": "Title", "metadata": {"page_number": 1}}, None),
            ExtractedElement(1, "Table", {"type": "Table", "text": "Table text", "metadata": {"page_number": 1}}, None),
        ]
        with patch("atlasrag_workers.unstructured_pdf.extract_elements", return_value=elements):
            self.worker.process(json.dumps(self.event).encode())
        args = self.worker._save_pages_and_complete.call_args.args
        self.assertEqual(args[2][0].text, "Title\n\nTable text")
        self.assertEqual(args[3], elements)
        self.assertTrue(args[4])

    def test_unstructured_error_propagates_without_completing(self):
        self.worker.pdf_extractor = "unstructured"
        with patch("atlasrag_workers.unstructured_pdf.extract_elements", side_effect=RuntimeError("model unavailable")):
            with self.assertRaisesRegex(RuntimeError, "model unavailable"):
                self.worker.process(json.dumps(self.event).encode())
        self.worker._save_pages_and_complete.assert_not_called()

    def test_chunking_error_propagates_without_completing(self):
        with patch("atlasrag_workers.consumer.chunk_pages", side_effect=ValueError("chunk failure")):
            with self.assertRaisesRegex(ValueError, "chunk failure"):
                self.worker.process(json.dumps(self.event).encode())
        self.worker._save_pages_and_complete.assert_not_called()

    def test_indexing_failure_does_not_complete_document(self):
        self.worker.indexer = Mock()
        self.worker.indexer.index.side_effect = RuntimeError("indexing failed")
        with self.assertRaisesRegex(RuntimeError, "indexing failed"):
            self.worker.process(json.dumps(self.event).encode())
        self.worker._save_pages_and_complete.assert_not_called()


if __name__ == "__main__":
    unittest.main()
