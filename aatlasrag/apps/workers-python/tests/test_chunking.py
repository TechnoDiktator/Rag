import unittest

from atlasrag_workers.chunking import MAX_CHARACTERS, chunk_elements, chunk_pages
from atlasrag_workers.consumer import PageText
from atlasrag_workers.unstructured_pdf import ExtractedElement


def element(category, text, page, image_key=None, metadata=None):
    return ExtractedElement(
        page, category,
        {"type": category, "text": text, "metadata": {"page_number": page, **(metadata or {})}},
        image_key,
    )


class ChunkingTests(unittest.TestCase):
    def test_pypdf_chunks_are_bounded_and_keep_the_page(self):
        text = "a" * (MAX_CHARACTERS * 2 + 5)
        chunks = chunk_pages([PageText(2, text), PageText(3, "")])
        self.assertEqual("".join(chunk.text for chunk in chunks), text)
        self.assertEqual([len(chunk.text) for chunk in chunks], [3000, 3000, 5])
        self.assertTrue(all(chunk.sources[0].page_number == 2 for chunk in chunks))
        self.assertTrue(all(chunk.sources[0].element_index is None for chunk in chunks))

    def test_title_chunks_preserve_pages_tables_and_images(self):
        elements = [
            element("Title", "First section", 1),
            element("NarrativeText", "First page", 1),
            element("Image", "", 2, "images/1"),
            element("NarrativeText", "Second page", 2),
            element("Table", "Revenue 100", 3, metadata={"text_as_html": "<table></table>"}),
        ]
        chunks = chunk_elements(elements)
        sources = [source for chunk in chunks for source in chunk.sources]
        self.assertEqual({source.element_index for source in sources}, set(range(5)))
        self.assertEqual({source.page_number for source in sources}, {1, 2, 3})
        self.assertTrue(any("Revenue 100" in chunk.text for chunk in chunks))
        self.assertTrue(all(len(chunk.text) <= MAX_CHARACTERS for chunk in chunks))

    def test_image_only_document_retains_image_reference(self):
        chunks = chunk_elements([element("Image", "", 1, "images/1")])
        self.assertEqual(len(chunks), 1)
        self.assertEqual(chunks[0].text, "")
        self.assertEqual(chunks[0].sources[0].element_index, 0)

    def test_oversized_element_is_split_without_losing_source(self):
        text = "x" * 6500
        chunks = chunk_elements([element("NarrativeText", text, 4)])
        self.assertEqual("".join(chunk.text for chunk in chunks), text)
        self.assertEqual([len(chunk.text) for chunk in chunks], [3000, 3000, 500])
        self.assertTrue(all(chunk.sources[0].element_index == 0 for chunk in chunks))

    def test_empty_elements_do_not_silently_complete(self):
        with self.assertRaisesRegex(ValueError, "produced no chunks"):
            chunk_elements([element("NarrativeText", "", 1)])


if __name__ == "__main__":
    unittest.main()
