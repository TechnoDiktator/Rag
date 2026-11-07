FROM python:3.12-slim

ENV PYTHONDONTWRITEBYTECODE=1
ENV PYTHONUNBUFFERED=1

WORKDIR /app

# Unstructured's PDF layout and OCR pipeline uses these native tools.
RUN apt-get update && \
    apt-get install -y --no-install-recommends poppler-utils tesseract-ocr libmagic1 libgl1 libglib2.0-0 && \
    rm -rf /var/lib/apt/lists/*

COPY pyproject.toml .
COPY src ./src

RUN pip install --no-cache-dir .

CMD ["python", "-m", "atlasrag_workers.consumer"]
