# 构建向量知识库

import argparse
from pathlib import Path
from sentence_transformers import SentenceTransformer
from langchain_community.vectorstores import Chroma
from langchain_core.embeddings import Embeddings
from langchain.schema import Document
from NL2SQL.rag.loader import parse_schema_md_by_table, parse_knowledge_from_db
from NL2SQL.rag.path_config import CHROMA_DB_DIR, HF_MODEL_DIR, RULES_CHROMA_DB_DIR, ensure_dirs

ensure_dirs()
class LocalBGEEmbeddings(Embeddings):
    def __init__(self, model_name=str(HF_MODEL_DIR)):
        self.model = SentenceTransformer(model_name)

    def embed_documents(self, texts: list[str]) -> list[list[float]]:
        return self.model.encode(texts, show_progress_bar=False).tolist()

    def embed_query(self, text: str) -> list[float]:
        return self.model.encode(text, show_progress_bar=False).tolist()

def build_chroma_from_md(md_path: str, persist_path: str = str(CHROMA_DB_DIR)):
    with open(md_path, "r", encoding="utf-8") as f:
        md_text = f.read()

    parsed_docs = parse_schema_md_by_table(md_text)
    if not parsed_docs:
        raise ValueError("No table documents found in schema documentation")
    documents = [
        Document(page_content=doc["content"], metadata=doc["metadata"])
        for doc in parsed_docs
    ]

    embedding = LocalBGEEmbeddings()
    db = Chroma.from_documents(documents, embedding, persist_directory=persist_path)
    db.persist()
    print(f" 本地向量库（按表切分）构建完成，保存路径：{persist_path}")


def build_chroma_from_knowledge(persist_path: str = str(RULES_CHROMA_DB_DIR)):
    """
    从 knowledge 表构建向量库
    """
    # 获取 knowledge 表的分块数据
    knowledge_chunks = parse_knowledge_from_db()

    if not knowledge_chunks:
        print("未找到 knowledge 表数据，向量库构建终止")
        return None

    # 转换为 Document 对象
    documents = [
        Document(page_content=doc["content"], metadata=doc["metadata"])
        for doc in knowledge_chunks
    ]

    # 创建嵌入模型
    embedding = LocalBGEEmbeddings()

    # 构建向量库
    db = Chroma.from_documents(
        documents=documents,
        embedding=embedding,
        persist_directory=persist_path
    )

    db.persist()
    print(f"Knowledge 向量库构建完成，共处理 {len(documents)} 条知识，保存路径：{persist_path}")
    return db



if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    targets = parser.add_mutually_exclusive_group()
    targets.add_argument("--rules-output", type=Path, metavar="NEW_DIRECTORY")
    targets.add_argument("--fields-output", type=Path, metavar="NEW_DIRECTORY")
    args = parser.parse_args()
    schema_doc = str(Path(__file__).resolve().parents[1] / "docs" / "schema_doc.md")
    output = args.rules_output or args.fields_output
    if output is not None:
        output = output.resolve()
        try:
            output.mkdir(parents=True, exist_ok=False)
        except FileExistsError:
            parser.error("output must be a new directory")
    if args.rules_output:
        if build_chroma_from_knowledge(str(output)) is None:
            raise SystemExit("No business rules indexed")
    elif args.fields_output:
        build_chroma_from_md(schema_doc, str(output))
    else:
        build_chroma_from_md(schema_doc)
