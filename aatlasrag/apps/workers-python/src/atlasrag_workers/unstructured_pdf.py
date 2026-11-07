"""Extract PDF layout elements and persist images outside the database."""

import base64
import hashlib
import io
from dataclasses import dataclass
from typing import Any

from minio import Minio
from unstructured.partition.pdf import partition_pdf


@dataclass(frozen=True)
class ExtractedElement:
    page_number: int
    category: str
    content: dict[str, Any]
    image_object_key: str | None


def extract_elements(
    content: bytes,
    document_id: str,
    version: int,
    page_count: int,
    object_client: Minio,
    bucket: str,
) -> list[ExtractedElement]:
    elements = partition_pdf(
        file=io.BytesIO(content),
        strategy="hi_res",
        infer_table_structure=True,
        extract_image_block_types=["Image"],
        extract_image_block_to_payload=True,
    )
    extracted = []
    for index, element in enumerate(elements):
        data = element.to_dict()
        metadata = data.get("metadata")
        if not isinstance(metadata, dict):
            raise ValueError(f"element {index} is missing metadata")
        page_number = metadata.get("page_number")
        if not isinstance(page_number, int) or not 1 <= page_number <= page_count:
            raise ValueError(f"element {index} has an invalid page number")
        category = data.get("type")
        if not isinstance(category, str) or not category:
            raise ValueError(f"element {index} is missing its category")

        encoded_image = metadata.pop("image_base64", None)
        image_key = None
        if category == "Image":
            if not isinstance(encoded_image, str) or not encoded_image:
                raise ValueError(f"image element {index} is missing its image payload")
            image = base64.b64decode(encoded_image, validate=True)
            digest = hashlib.sha256(image).hexdigest()
            image_key = f"documents/{document_id}/v{version}/elements/{index}/{digest}"
            image_type = metadata.get("image_mime_type") or "application/octet-stream"
            object_client.put_object(
                bucket, image_key, io.BytesIO(image), len(image), content_type=image_type
            )
        elif encoded_image:
            raise ValueError(f"unexpected image payload on element {index}")

        extracted.append(ExtractedElement(page_number, category, data, image_key))
    return extracted
