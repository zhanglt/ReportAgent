"""Local HTTP adapter for the existing BGE and Chroma retrievers."""

from contextlib import asynccontextmanager

import chromadb
from fastapi import FastAPI, Request
from langchain_community.vectorstores import Chroma
from pydantic import BaseModel, Field

from NL2SQL.rag.path_config import CHROMA_DB_DIR, RULES_CHROMA_DB_DIR
from NL2SQL.rag.retriever import (
    LocalBGEEmbeddings,
    get_field_docs_by_tables,
    get_rules_by_knowledgecontent,
)


def open_index(directory, embedding):
    if not (directory / "chroma.sqlite3").is_file():
        raise RuntimeError("Missing Chroma index at " + str(directory))
    client = chromadb.PersistentClient(path=str(directory))
    client.get_collection("langchain")
    return Chroma(client=client, embedding_function=embedding)


@asynccontextmanager
async def lifespan(app: FastAPI):
    embedding = LocalBGEEmbeddings()
    app.state.fields = open_index(CHROMA_DB_DIR, embedding)
    app.state.rules = open_index(RULES_CHROMA_DB_DIR, embedding)
    yield


app = FastAPI(lifespan=lifespan)


class FieldRequest(BaseModel):
    table_names: list[str] = Field(min_length=1, max_length=32)


class RulesRequest(BaseModel):
    query: str = Field(min_length=1, max_length=16000)


@app.get("/health")
def health():
    return {"status": "ok"}


@app.post("/field-docs")
def fields(body: FieldRequest, request: Request):
    docs = get_field_docs_by_tables(body.table_names, db=request.app.state.fields)
    return {"documents": [doc.page_content for doc in docs]}


@app.post("/rules")
def rules(body: RulesRequest, request: Request):
    docs = get_rules_by_knowledgecontent(body.query, score_threshold=0.5, db=request.app.state.rules)
    return {"documents": [doc.page_content for doc in docs]}
