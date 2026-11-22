#!/usr/bin/env python3
"""将 Eino Dev 的实际画布转换为 Eino Workflow 可导入的拓扑 schema。"""

import argparse
import copy
import json
from pathlib import Path
from urllib.request import urlopen


def fetch(url):
    with urlopen(url, timeout=15) as response:
        result = json.load(response)
    if result.get("code") != 0:
        raise ValueError(f"Eino Dev request failed: {result.get('msg')}")
    return result["data"]


def graph_schema(canvas):
    # 调试画布用 key 标识节点；编排导入通过 id 和 WorkflowNodeId 连接节点。
    nodes = {node["key"]: node for node in canvas["nodes"]}
    edges = canvas["edges"]
    if not nodes or not edges:
        raise ValueError("Eino Dev returned an empty graph")
    branches = {key for key, node in nodes.items() if node["type"] == "branch"}
    ids = {key: key.replace(":", "_") for key in nodes}
    positions = {
        "start": (0, 0), "intent_analysis": (360, 0),
        "template_query_and_split": (720, 0), "scheduler": (1080, 0),
        "from:scheduler": (1440, 0), "nl2sql": (1800, 0),
        "result_review": (2160, 0), "from:result_review": (2520, 0),
        "write_report": (1800, 600), "end": (2160, 600),
    }

    def layout(key):
        x, y = positions.get(key, (360 * sorted(nodes).index(key), 1000))
        return {"position": {"x": x, "y": y}}

    output = {
        "id": "report", "name": canvas["name"], "component": "Graph",
        "node_trigger_mode": "AnyPredecessor", "nodes": [], "edges": [], "branches": [],
    }
    start = nodes["start"]["component_schema"]
    output["input_type"] = copy.deepcopy(start["input_type"])
    output["output_type"] = copy.deepcopy(start["output_type"])
    for key, node in sorted(nodes.items()):
        if key in branches:
            incoming = [e["source_node_key"] for e in edges if e["target_node_key"] == key]
            targets = sorted(e["target_node_key"] for e in edges if e["source_node_key"] == key)
            if len(incoming) != 1 or not targets:
                raise ValueError(f"Invalid branch: {key}")
            source = incoming[0]
            output["branches"].append({
                "id": ids[key], "source_node_key": source, "target_node_keys": targets,
                "sourceWorkflowNodeId": ids[source],
                "targetWorkflowNodeIds": [ids[target] for target in targets],
                # 这里只描述类型；Go 分支判断函数无法从画布反向恢复。
                "condition": {"input_type": copy.deepcopy(output["input_type"]), "is_stream": False},
                "layoutData": layout(key),
            })
            continue
        item = copy.deepcopy(node)
        item["id"] = ids[key]
        item["layoutData"] = layout(key)
        output["nodes"].append(item)
    for edge in sorted(edges, key=lambda e: (e["source_node_key"], e["target_node_key"])):
        source, target = edge["source_node_key"], edge["target_node_key"]
        if source in branches or target in branches:
            continue
        output["edges"].append({
            "id": f"{ids[source]}_to_{ids[target]}", "name": edge.get("name", ""),
            "source_node_key": source, "target_node_key": target,
            "sourceWorkflowNodeId": ids[source], "targetWorkflowNodeId": ids[target],
        })
    return output


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", default="http://127.0.0.1:52538", help="Eino Dev 服务地址")
    parser.add_argument("--output", type=Path, default=Path(__file__).resolve().parents[1] / "report-go/graphs/report.graph.json")
    args = parser.parse_args()
    base = args.url.rstrip("/") + "/eino/devops/debug/v1/graphs"
    graphs = fetch(base)["graphs"]
    matches = [graph for graph in graphs if graph["name"] == "report"]
    if len(matches) != 1:
        raise ValueError("Expected one report graph; start report-agent with EINO_DEV=1")
    canvas = fetch(base + "/" + matches[0]["id"] + "/canvas")["canvas_info"]
    schema = graph_schema(canvas)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(schema, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"Exported {args.output}: {len(schema['nodes'])} nodes, {len(schema['branches'])} branches")


if __name__ == "__main__":
    main()
