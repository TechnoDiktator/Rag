import os
from langchain_community.document_loaders import TextLoader, DirectoryLoader
from langchain_text_splitters import CharacterTextSplitter
from langchain_openai import OpenAIEmbeddings
from langchain_chroma import Chroma
from dotenv import load_dotenv


load_dotenv()

# we need the embedding model to create vector embeddings of the documents
#  Chroma is the vector database that will store the embeddings and allow us to perform similarity searches


# this function loads documents from the specified directory using DirectoryLoader and returns a list of documents
def load_documents(docs_path="docs"):
    """
    Load documents from the specified directory using DirectoryLoader.
    """
    if not os.path.exists(docs_path):
        raise FileNotFoundError(f"The specified documents path '{docs_path}' does not exist.")


    loader = DirectoryLoader(docs_path, 
                             glob="**/*.txt", loader_cls=TextLoader,
                             loader_kwargs={"encoding": "utf-8"})
    documents = loader.load()

    if len(documents) == 0:
        raise FileNotFoundError(f"No documents found in the specified path '{docs_path}'.")

    for i , doc in enumerate(documents):
        print(f"Loaded document {i + 1}: {doc.metadata.get('source', 'Unknown source')}")
        print(f" Source: {doc.metadata.get('source', 'Unknown source')}")
        print(f" Content length: {len(doc.page_content)} characters")
        print(f" Content preview: {doc.page_content[:100]}...")  # Print first 100 characters of content
        print(f" Metadata: {doc.metadata}")

    return documents


# what is chunk size and chunk overlap?
# Chunk size refers to the maximum number of characters in each chunk of text.
# Chunk overlap refers to the number of characters that should overlap between consecutive chunks.
# why is chunk overlap important? Chunk overlap is important because it helps maintain context between chunks. When a document is split into smaller pieces, some information may be lost at the boundaries of the chunks. By allowing for overlap, we can ensure that important context is preserved, which can improve the quality of embeddings and subsequent retrievals.
def split_documents(documents, chunk_size=1000, chunk_overlap=0):
    """Split documents into smaller chunks with overlap"""
    print("Splitting documents into chunks...")
    
    text_splitter = CharacterTextSplitter(
        chunk_size=chunk_size, 
        chunk_overlap=chunk_overlap
    )
    
    chunks = text_splitter.split_documents(documents)
    
    if chunks:

        print(f"Total chunks created: {len(chunks)}")
        # print the first 5 chunks for inspection
        for i, chunk in enumerate(chunks[:5]):
            print(f"\n--- Chunk {i+1} ---")
            print(f"Source: {chunk.metadata['source']}")
            print(f"Length: {len(chunk.page_content)} characters")
            print(f"Content:")
            print(chunk.page_content)
            print("-" * 50)

        # print the number of additional chunks if there are more than 5
        if len(chunks) > 5:
            print(f"\n... and {len(chunks) - 5} more chunks")
    
    return chunks



# this method creates a vector store using ChromaDB and persists it to the specified directory. 
# It takes the chunks of documents as input, generates embeddings for each chunk using OpenAIEmbeddings, 
# and then stores them in the ChromaDB vector store.
def create_vector_store(chunks, persist_directory="db/chroma_db"):
    """Create and persist ChromaDB vector store"""
    print("Creating embeddings and storing in ChromaDB...")
        
    embedding_model = OpenAIEmbeddings(model="text-embedding-3-small")
    
    # Create ChromaDB vector store
    print("--- Creating vector store ---")
    vectorstore = Chroma.from_documents(
        documents=chunks,
        embedding=embedding_model,
        persist_directory=persist_directory, 
        collection_metadata={"hnsw:space": "cosine"}
    )
    print("--- Finished creating vector store ---")
    
    print(f"Vector store created and saved to {persist_directory}")
    return vectorstore


def main():

    
    # 1. Load documents from the specified directory
    # 2. Chunk the documents into smaller pieces
    # 3. Create embeddings for each chunk and store them in the vector database
    """Main ingestion pipeline"""
    print("=== RAG Document Ingestion Pipeline ===\n")
    
    # Define paths
    docs_path = "docs"
    persistent_directory = "db/chroma_db"
    
    # Check if vector store already exists
    if os.path.exists(persistent_directory):
        print("✅ Vector store already exists. No need to re-process documents.")
        
        embedding_model = OpenAIEmbeddings(model="text-embedding-3-small")
        vectorstore = Chroma(
            persist_directory=persistent_directory,
            embedding_function=embedding_model, 
            collection_metadata={"hnsw:space": "cosine"}
        )
        print(f"Loaded existing vector store with {vectorstore._collection.count()} documents")
        return vectorstore
    
    print("Persistent directory does not exist. Initializing vector store...\n")
    
    # Step 1: Load documents
    documents = load_documents(docs_path)  

    # Step 2: Split into chunks
    chunks = split_documents(documents)
    
    # # Step 3: Create vector store
    vectorstore = create_vector_store(chunks, persistent_directory)
    
    print("\n✅ Ingestion complete! Your documents are now ready for RAG queries.")
    return vectorstore





if __name__ == "__main__":
    main()


#  this is what documnets is going to look like 


# documents = [
#    Document(
#        page_content="Google LLC is an American multinational corporation and technology company focusing on online advertising, search engine technology, cloud computing, computer software, quantum computing, e-commerce, consumer electronics, and artificial intelligence (AI).",
#        metadata={'source': 'docs/google.txt'}
#    ),
#    Document(
#        page_content="Microsoft Corporation is an American multinational corporation and technology conglomerate headquartered in Redmond, Washington.",
#        metadata={'source': 'docs/microsoft.txt'}
#    ),
#    Document(
#        page_content="Nvidia Corporation is an American technology company headquartered in Santa Clara, California.",
#        metadata={'source': 'docs/nvidia.txt'}
#    ),
#    Document(
#        page_content="Space Exploration Technologies Corp., commonly referred to as SpaceX, is an American space technology company headquartered at the Starbase development site in Starbase, Texas.",
#        metadata={'source': 'docs/spacex.txt'}
#    ),
#    Document(
#        page_content="Tesla, Inc. is an American multinational automotive and clean energy company headquartered in Austin, Texas.",
#        metadata={'source': 'docs/tesla.txt'}
#    )
# ]
