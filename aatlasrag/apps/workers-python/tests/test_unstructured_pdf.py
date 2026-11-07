import base64
import hashlib
import unittest
from unittest.mock import Mock, patch

from atlasrag_workers.unstructured_pdf import extract_elements


class UnstructuredExtractionTests(unittest.TestCase):
    def test_preserves_table_and_uploads_image_without_base64_in_metadata(self):
        table = Mock()
        table.to_dict.return_value = {
            "type": "Table",
            "text": "Revenue",
            "metadata": {"page_number": 1, "text_as_html": "<table></table>"},
        }
        image = Mock()
        image.to_dict.return_value = {
            "type": "Image",
            "metadata": {
                "page_number": 2,
                "image_mime_type": "image/jpeg",
                "image_base64": base64.b64encode(b"image-bytes").decode(),
            },
        }
        store = Mock()
        with patch("atlasrag_workers.unstructured_pdf.partition_pdf", return_value=[table, image]) as partition:
            result = extract_elements(b"pdf", "doc-id", 1, 2, store, "bucket")

        self.assertEqual(partition.call_args.kwargs["strategy"], "hi_res")
        self.assertEqual(result[0].content["metadata"]["text_as_html"], "<table></table>")
        digest = hashlib.sha256(b"image-bytes").hexdigest()
        self.assertEqual(result[1].image_object_key, f"documents/doc-id/v1/elements/1/{digest}")
        self.assertNotIn("image_base64", result[1].content["metadata"])
        self.assertEqual(store.put_object.call_args.args[1], result[1].image_object_key)
        self.assertEqual(store.put_object.call_args.args[2].getvalue(), b"image-bytes")

    def test_rejects_element_with_invalid_page_number(self):
        element = Mock()
        element.to_dict.return_value = {"type": "Title", "metadata": {"page_number": 3}}
        with patch("atlasrag_workers.unstructured_pdf.partition_pdf", return_value=[element]):
            with self.assertRaisesRegex(ValueError, "page number"):
                extract_elements(b"pdf", "doc-id", 1, 2, Mock(), "bucket")

    def test_rejects_missing_image_payload(self):
        element = Mock()
        element.to_dict.return_value = {"type": "Image", "metadata": {"page_number": 1}}
        with patch("atlasrag_workers.unstructured_pdf.partition_pdf", return_value=[element]):
            with self.assertRaisesRegex(ValueError, "missing its image"):
                extract_elements(b"pdf", "doc-id", 1, 1, Mock(), "bucket")

    def test_propagates_object_store_failure(self):
        element = Mock()
        element.to_dict.return_value = {
            "type": "Image",
            "metadata": {"page_number": 1, "image_base64": base64.b64encode(b"image").decode()},
        }
        store = Mock()
        store.put_object.side_effect = RuntimeError("store unavailable")
        with patch("atlasrag_workers.unstructured_pdf.partition_pdf", return_value=[element]):
            with self.assertRaisesRegex(RuntimeError, "store unavailable"):
                extract_elements(b"pdf", "doc-id", 1, 1, store, "bucket")
