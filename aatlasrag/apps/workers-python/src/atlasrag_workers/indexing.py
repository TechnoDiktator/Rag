"""Generate searchable descriptions and vectors for document chunks."""

import base64
import math
from dataclasses import dataclass
from typing import TYPE_CHECKING

from minio import Minio
from openai import OpenAI
from openai.types.chat import ChatCompletionContentPartParam

from atlasrag_workers.chunking import DocumentChunk

if TYPE_CHECKING:
    from atlasrag_workers.unstructured_pdf import ExtractedElement

EMBEDDING_MODEL = "text-embedding-3-small"
VISION_MODEL = "gpt-4o"
EMBEDDING_DIMENSIONS = 1536
MAX_IMAGE_BYTES = 10 * 1024 * 1024
MAX_IMAGES_PER_CHUNK = 10
MAX_TABLE_CHARS = 30000
IMAGE_TYPES = {"image/jpeg", "image/png", "image/webp", "image/gif"}


@dataclass(frozen=True)
class IndexedChunk:
    description: str | None
    searchable_text: str
    embedding: list[float]


class OpenAIIndexer:
    def __init__(self, client: OpenAI, object_client: Minio, bucket: str) -> None:
        self.client = client
        self.object_client = object_client
        self.bucket = bucket

    def index(
        self, chunks: list[DocumentChunk], elements: list["ExtractedElement"] | None
    ) -> list[IndexedChunk]:
        if not chunks:
            raise ValueError("cannot index a document with no chunks")
        results = []
        for chunk in chunks:
            images: list[tuple[str, bytes]] = []
            tables: list[str] = []
            for source in chunk.sources:
                if source.element_index is None:
                    continue
                if elements is None or not 0 <= source.element_index < len(elements):
                    raise ValueError("chunk references an unknown extracted element")
                element = elements[source.element_index]
                if element.category == "Table":
                    metadata = element.content.get("metadata")
                    html = metadata.get("text_as_html") if isinstance(metadata, dict) else None
                    if not isinstance(html, str) or not html:
                        raise ValueError("table element is missing its HTML")
                    tables.append(html)
                if element.image_object_key:
                    metadata = element.content.get("metadata")
                    mime = metadata.get("image_mime_type") if isinstance(metadata, dict) else None
                    if not isinstance(mime, str) or mime not in IMAGE_TYPES:
                        raise ValueError("image element has an unsupported MIME type")
                    response = self.object_client.get_object(self.bucket, element.image_object_key)
                    try:
                        data = response.read(MAX_IMAGE_BYTES + 1)
                    finally:
                        response.close()
                        response.release_conn()
                    if not data or len(data) > MAX_IMAGE_BYTES:
                        raise ValueError("image is empty or exceeds the size limit")
                    images.append((mime, data))

            if len(images) > MAX_IMAGES_PER_CHUNK:
                raise ValueError("chunk exceeds the image count limit")
            if sum(len(table) for table in tables) > MAX_TABLE_CHARS:
                raise ValueError("chunk exceeds the table HTML size limit")

            description = None
            if images or tables:
                content: list[ChatCompletionContentPartParam] = [{
                    "type": "text",
                    "text": (
                        "Describe the images and tables for document retrieval. "
                        "Include important facts, labels, numbers and relationships. "
                        "Do not invent details.\n\n"
                        f"Context:\n{chunk.text}\n\nTables:\n" + "\n\n".join(tables)
                    ),
                }]
                for mime, data in images:
                    encoded = base64.b64encode(data).decode("ascii")
                    content.append({
                        "type": "image_url",
                        "image_url": {"url": f"data:{mime};base64,{encoded}"},
                    })
                response = self.client.chat.completions.create(
                    model=VISION_MODEL,
                    messages=[{"role": "user", "content": content}],
                    max_tokens=1024,
                    temperature=0,
                )
                if not response.choices or response.choices[0].finish_reason != "stop":
                    raise ValueError("vision model did not return a complete description")
                description = response.choices[0].message.content
                if not isinstance(description, str) or not description.strip():
                    raise ValueError("vision model returned an empty description")

            searchable_text = "\n\n".join(part for part in (chunk.text, description) if part and part.strip())
            if not searchable_text:
                raise ValueError("chunk has no searchable text")
            response = self.client.embeddings.create(
                model=EMBEDDING_MODEL,
                input=searchable_text,
                dimensions=EMBEDDING_DIMENSIONS,
            )
            if len(response.data) != 1 or len(response.data[0].embedding) != EMBEDDING_DIMENSIONS:
                raise ValueError("embedding response has unexpected dimensions")
            embedding = response.data[0].embedding
            if not all(math.isfinite(value) for value in embedding):
                raise ValueError("embedding response contains non-finite values")
            results.append(IndexedChunk(description, searchable_text, embedding))
        return results
