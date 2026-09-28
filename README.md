# Retrieval-Augmented Generation (RAG) Demo

A small RAG project that indexes company reference documents, retrieves the
most relevant text for a natural-language question, and uses an OpenAI chat
model to generate answers from that context. It also includes a history-aware
conversation mode that rewrites follow-up questions before searching, plus a
notebook-based multi-modal RAG workflow for PDF documents that include text,
images, and tables.

## Project Flow

```mermaid
flowchart TD
	A[Text files in project/docs] --> B[DirectoryLoader]
	B --> C[CharacterTextSplitter<br/>chunk size: 1000]
	C --> D[OpenAI text-embedding-3-small]
	D --> E[(ChromaDB<br/>project/db/chroma_db)]

	P[PDF in multi_modal_rag/docs] --> P1[Unstructured PDF parsing]
	P1 --> P2[Title-based chunking]
	P2 --> P3[Text + tables + images extracted]
	P3 --> D

	Q[User query] --> R[Chroma retriever<br/>top 5 results]
	E --> R
	R --> S[Relevant document chunks]
	S --> T[Prompt with retrieved context]
	T --> U[ChatOpenAI gpt-4o]
	U --> V[Generated answer]

	H[Conversation history] --> W[Rewrite follow-up question]
	Q --> W
	W --> R3[Chroma retriever<br/>top 3 results]
	E --> R3
	R3 --> X[History-aware prompt]
	H --> X
	X --> Y[ChatOpenAI gpt-4o]
	Y --> Z[Answer and updated history]
```

## Repository Layout

```text
Rag/
├── README.md
├── project/
│   ├── 1_ingestion_pipeline.py
│   ├── 2_retreival_pipeline.py
│   ├── 3_answer_generation.py
│   ├── 4_history_aware_rag_with_generation.py
│   ├── docs/                 # Source .txt documents
│   └── db/chroma_db/         # Persisted Chroma vector store
└── multi_modal_rag/
    ├── 8_multi_modal_rag.ipynb
    ├── docs/                 # PDF source documents
    ├── images/               # Extracted image outputs
    ├── requirements.txt
    └── multi_modal_rag_env/  # Optional environment folder
```

## How It Works

1. `1_ingestion_pipeline.py` loads every `.txt` file from `project/docs/`.
2. Documents are split into chunks of up to 1,000 characters.
3. `text-embedding-3-small` converts each chunk into a vector embedding.
4. ChromaDB stores the embeddings in `project/db/chroma_db/` using cosine similarity.
5. `2_retreival_pipeline.py` loads the persisted store and retrieves the five most relevant chunks for its query.
6. `3_answer_generation.py` adds the retrieved chunks to a prompt and asks `gpt-4o` to produce an answer.
7. `4_history_aware_rag_with_generation.py` uses conversation history to rewrite follow-up questions, retrieves three relevant chunks, generates an answer, and stores the exchange in memory.
8. `multi_modal_rag/8_multi_modal_rag.ipynb` handles PDF-based RAG: it parses PDFs with `unstructured`, extracts tables and images, chunks the content with title-based logic, and stores the enriched chunks in Chroma for retrieval.

The included documents cover Google, Microsoft, Nvidia, SpaceX, and Tesla.

## Multimodal Notebook Architecture

The notebook workflow is designed for PDFs that contain mixed content such as
paragraph text, tables, and embedded images. It uses `unstructured` to parse
raw PDF elements, chunks those elements by title/structure, and stores the
resulting content in a Chroma vector database for retrieval.

```mermaid
flowchart LR
    A[PDF Document] --> B[unstructured.partition_pdf]
    B --> C[Extracted elements]
    C --> D[Title-based chunking]
    D --> E[Text chunks]
    D --> F[Table blocks]
    D --> G[Image blocks]
    E --> H[Combined content metadata]
    F --> H
    G --> H
    H --> I[OpenAI embeddings]
    I --> J[(Chroma vector store)]
    K[User question] --> L[Retriever]
    J --> L
    L --> M[Relevant multimodal chunks]
    M --> N[ChatOpenAI answer generation]
    N --> O[Final answer]
```

### Multimodal Flow Summary

- PDF input is parsed into structured elements.
- The notebook separates text, tables, and images.
- Content is summarized and chunked into retrieval-friendly units.
- Embeddings are created and saved into Chroma.
- Retrieved chunks are passed to the model for grounded answer generation.

## Setup

Create a `.env` file in `project/` with an OpenAI API key:

```env
OPENAI_API_KEY=your_api_key_here
```

The project dependencies are already present in the included virtual
environment. To use a different environment, install the packages imported by
the scripts, including `langchain-community`, `langchain-text-splitters`,
`langchain-openai`, `langchain-chroma`, and `python-dotenv`.

The generation scripts require access to both the OpenAI embedding model
`text-embedding-3-small` and the chat model `gpt-4o`. API usage may incur costs.

## Run

Run commands from the `project/` directory so the relative paths resolve
correctly:

```powershell
cd project
python 1_ingestion_pipeline.py
python 2_retreival_pipeline.py
python 3_answer_generation.py
python 4_history_aware_rag_with_generation.py
```

For the notebook-based multimodal workflow, open:

```text
multi_modal_rag/8_multi_modal_rag.ipynb
```

This notebook expects a PDF file in `multi_modal_rag/docs/`, installs the
required packages, and uses `unstructured` to parse PDF content, extract
images and tables, and build a vector store for retrieval.

The ingestion script skips rebuilding the database when
`db/chroma_db/` already exists. Remove that directory before rerunning
ingestion if the source documents have changed.

## Example Query

The retrieval script currently searches for:

```text
How much did Microsoft pay to acquire GitHub?
```

It prints the query and the retrieved document chunks as context.

The answer-generation script uses the same query and prints a model-generated
response based only on the retrieved chunks. The history-aware script starts an
interactive session; type `quit` to exit. Its conversation history is kept in
memory for the duration of that process and is not persisted to disk.
