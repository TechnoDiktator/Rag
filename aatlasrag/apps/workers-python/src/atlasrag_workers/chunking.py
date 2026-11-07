"""Build bounded chunks while retaining links to the extracted source."""

from dataclasses import dataclass
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from atlasrag_workers.consumer import PageText
    from atlasrag_workers.unstructured_pdf import ExtractedElement

MAX_CHARACTERS = 3000
NEW_AFTER_N_CHARS = 2400
COMBINE_TEXT_UNDER_N_CHARS = 500


@dataclass(frozen=True)
class ChunkSource:
    page_number: int
    element_index: int | None


@dataclass(frozen=True)
class DocumentChunk:
    text: str
    sources: list[ChunkSource]


def chunk_pages(pages: list["PageText"]) -> list[DocumentChunk]:
    chunks = []
    for page in pages:
        if not page.text.strip():
            continue
        for offset in range(0, len(page.text), MAX_CHARACTERS):
            text = page.text[offset:offset + MAX_CHARACTERS]
            chunks.append(DocumentChunk(text, [ChunkSource(page.page_number, None)]))
    return chunks


def chunk_elements(elements: list["ExtractedElement"]) -> list[DocumentChunk]:
    from unstructured.chunking.title import chunk_by_title
    from unstructured.staging.base import dict_to_elements

    data = [
        {**element.content, "element_id": str(index)}
        for index, element in enumerate(elements)
    ]
    parsed = dict_to_elements(data)
    if len(parsed) != len(elements):
        raise ValueError("could not reconstruct all extracted elements for chunking")

    raw_chunks = chunk_by_title(
        parsed,
        max_characters=MAX_CHARACTERS,
        new_after_n_chars=NEW_AFTER_N_CHARS,
        combine_text_under_n_chars=COMBINE_TEXT_UNDER_N_CHARS,
        include_orig_elements=True,
    )
    ordered: list[tuple[int, DocumentChunk]] = []
    seen: set[int] = set()
    for raw in raw_chunks:
        sources = []
        for original in raw.metadata.orig_elements or []:
            try:
                index = int(original.id)
            except (TypeError, ValueError) as exc:
                raise ValueError("chunk has an invalid source element ID") from exc
            if not 0 <= index < len(elements):
                raise ValueError("chunk references an unknown source element")
            source = ChunkSource(elements[index].page_number, index)
            if source not in sources:
                sources.append(source)
            seen.add(index)
        if not sources:
            raise ValueError("chunk is missing its source elements")
        ordered.append((min(source.element_index for source in sources if source.element_index is not None),
                        DocumentChunk(raw.text, sources)))

    for index, element in enumerate(elements):
        if element.image_object_key and index not in seen:
            ordered.append((index, DocumentChunk("", [ChunkSource(element.page_number, index)])))
    if elements and not ordered:
        raise ValueError("Unstructured elements produced no chunks or image references")
    ordered.sort(key=lambda item: item[0])
    return [chunk for _, chunk in ordered]
