# Repository Guidelines

## Project Structure

The root Python modules implement report generation. `graph.py` defines the LangGraph workflow and serves WebSocket requests. `node.py` implements workflow steps, `state.py` defines shared state, and `template_planner.py` and `report_writer.py` handle report planning and writing. Report templates live in `templates/`.

`NL2SQL/` contains the FastAPI query service, database configuration, schema cache, retrieval code in `rag/`, and Neo4j join planning in `knowledge_graph/`. Update `NL2SQL/docs/schema_doc.md` when field descriptions change. Tests and interactive checks live in `test/`.

## Local Development

Run commands from the repository root. Use a Python virtual environment, install dependencies with `python -m pip install -r requirements.txt`, and configure `.env` using the README. The pinned requirements include platform-specific Windows and CUDA packages, so use compatible packages for your development platform.

- `python -m uvicorn NL2SQL.agent_mod:app --host 127.0.0.1 --port 8001` starts the query service. Check it with `curl http://127.0.0.1:8001/health`.
- `python graph.py` starts the report workflow at `ws://localhost:8080`.

## Coding Style

Use four spaces for indentation, `snake_case` for Python functions and variables, `PascalCase` for classes, and `UPPER_SNAKE_CASE` for environment variables. Follow nearby type annotations and keep shared workflow fields in `state.py`.

## Testing

Run automated `unittest` cases with `python -m unittest discover -s test -p 'test_*.py'`. Name new test files and methods `test_*`. Run `python test/test_node_1.py` or `python test/test_node_5.py` and select `mock` or `fallback` for interactive checks without a live model. The node 2 and node 6 scripts call live services.

## Commits and Pull Requests

Recent commits use short imperative subjects such as "Ignore local test data." In pull requests, explain the behavior change, list verification commands and results, and link related issues when available. Call out changes to configuration, schemas, or API responses.

## Configuration and Data

Keep credentials in local environment variables or `.env`. Use disposable databases for initialization. The graph builder clears the configured Neo4j database, and `NL2SQL/waterknow.sql` drops existing tables.
