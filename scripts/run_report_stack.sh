#!/usr/bin/env bash
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"
python=${REPORT_PYTHON:-python}
mode=${1:-go}
if [[ "$mode" != go && "$mode" != python ]]; then
    printf 'Usage: %s [go|python]\n' "$0" >&2
    exit 2
fi

export NL2SQL_STREAMING=${NL2SQL_STREAMING:-1}
export MAIN_LLM_STREAMING=${MAIN_LLM_STREAMING:-1}
path=$("$python" -c 'from NL2SQL.config.settings import SERVICE_PATH; print(SERVICE_PATH)')
if [[ "$path" != /* || "$path" == *[$' \t\n{}?#']* ]]; then
    printf 'Invalid NL2SQL_PATH\n' >&2
    exit 2
fi

pids=()
build_dir=
cleanup() {
    trap - EXIT INT TERM
    for pid in "${pids[@]}"; do
        kill -TERM "$pid" 2>/dev/null || true
    done
    for pid in "${pids[@]}"; do
        wait "$pid" 2>/dev/null || true
    done
    if [[ -n "$build_dir" ]]; then
        "$python" -c 'import shutil,sys; shutil.rmtree(sys.argv[1])' "$build_dir"
    fi
}
trap cleanup EXIT
trap 'exit 0' INT TERM
start() {
    "$@" &
    pids+=("$!")
}
wait_health() {
    local url=$1 pid=$2
    for ((attempt=0; attempt<120; attempt++)); do
        kill -0 "$pid" 2>/dev/null || return 1
        if curl -fsS --max-time 1 "$url" >/dev/null 2>&1; then
            return 0
        fi
        sleep 0.5
    done
    printf 'Health check timed out for %s\n' "$url" >&2
    return 1
}

if [[ "$mode" == go ]]; then
    build_dir=$(mktemp -d)
    go -C report-go build -o "$build_dir/nl2sql" ./cmd/nl2sql
    go -C report-go build -o "$build_dir/report-agent" ./cmd/report-agent
    export REPORT_LISTEN_ADDR=${REPORT_LISTEN_ADDR:-127.0.0.1:8080}
    export NL2SQL_GO_LISTEN_ADDR=${NL2SQL_GO_LISTEN_ADDR:-127.0.0.1:8001}
    export NL2SQL_RETRIEVAL_URL=http://127.0.0.1:${REPORT_RAG_PORT:-18082}
    export NL2SQL_URL=http://${NL2SQL_GO_LISTEN_ADDR}$path
    start "$python" -m uvicorn NL2SQL.rag.service:app --host 127.0.0.1 --port "${REPORT_RAG_PORT:-18082}"
    wait_health "$NL2SQL_RETRIEVAL_URL/health" "$!"
    start "$build_dir/nl2sql"
    wait_health "http://${NL2SQL_GO_LISTEN_ADDR}/health" "$!"
    start "$build_dir/report-agent"
    wait_health "http://${REPORT_LISTEN_ADDR}/health" "$!"
else
    port=${REPORT_PYTHON_QUERY_PORT:-8001}
    export NL2SQL_URL=http://127.0.0.1:$port$path
    start "$python" -m uvicorn NL2SQL.agent_mod:app --host 127.0.0.1 --port "$port"
    wait_health "http://127.0.0.1:$port/health" "$!"
    start "$python" graph.py
fi
wait -n "${pids[@]}"
