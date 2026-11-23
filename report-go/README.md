# Go 报告工作流

`internal/report/workflow.go` 使用 Eino Graph 编排报告流程，包含六个业务节点、条件分支和查询循环。启动与迁移说明见[重构文档](../docs/eino-refactor-plan.md)。

## 启动 Go 报告和查询服务

从仓库根目录执行以下命令。脚本依次启动 Python 检索、Go NL2SQL 和 Go 报告服务，并配置报告使用的查询地址。

```bash
REPORT_LISTEN_ADDR=127.0.0.1:18080 \
  NL2SQL_GO_LISTEN_ADDR=127.0.0.1:18081 \
  scripts/run_report_stack.sh go
```

分别启动时，先启动 Python 检索（默认端口 `18082`）和 Go NL2SQL（默认端口 `18081`），再以 `NL2SQL_URL=http://127.0.0.1:18081/nl2sql` 启动报告进程。报告程序默认访问 `8001`，适合该端口已运行 Python NL2SQL 的配置。报告的 `/health` 仅检查自身，查询和检索分别可通过 `http://127.0.0.1:18081/health` 和 `http://127.0.0.1:18082/health` 检查。

单独运行 Go NL2SQL 时，也必须保持 Python 检索服务运行。在仓库根目录、已安装 Python 依赖的环境中执行：

```bash
python -m uvicorn NL2SQL.rag.service:app --host 127.0.0.1 --port 18082
curl -fsS http://127.0.0.1:18082/health
```

Go 通过 `NL2SQL_RETRIEVAL_URL` 连接检索服务（默认 `http://127.0.0.1:18082`）。若提示字段说明或业务规则检索服务不可用，检查该地址、Python 启动日志及 BGE/Chroma 索引配置。查询服务自身的 `/health` 返回成功，并不代表检索依赖可用。

`REPORT_MODEL_TIMEOUT_SECONDS` 控制单次报告模型调用（默认 180 秒），`REPORT_TIMEOUT_SECONDS` 控制整份报告（默认 1200 秒）。失败日志中的 `stage_timeout=true` 表示当前阶段收到超时错误，`timeout=true` 表示整份报告达到期限。

## 模型推理强度

在根目录 `.env` 中分别设置报告模型与 NL2SQL 模型的推理强度：

```dotenv
MODEL_REASONING_EFFORT=high
NL2SQL_REASONING_EFFORT=medium
```

`MODEL_REASONING_EFFORT` 对应 `MODEL`，`NL2SQL_REASONING_EFFORT` 对应 `NL2SQL_MODEL`，两者独立配置。修改后重启相应 Go 服务。未设置或留空时不发送该字段，使用上游默认行为。

客户端接受 `none`、`minimal`、`low`、`medium`、`high`、`xhigh`、`max`，具体模型和兼容网关支持的档位需单独确认；不支持时可能返回 HTTP 400。强度越高通常耗时及 token 消耗越大，必要时调整对应模型的调用超时。参数通过 Chat Completions 的 `reasoning_effort` 字段发送，适用于普通输出、JSON 输出、工具调用及流式调用。第三方网关是否实际执行该参数需要实测。

## 在 VS Code Eino Dev 中展示

在仓库根目录运行，使用已有的 `.env` 配置：

```bash
cd report-go
EINO_DEV=1 go run ./cmd/report-agent
```

程序在编译图之前初始化 Eino Dev，注册名为 `report` 的图。无需提交报告请求即可查看拓扑；保持进程运行。

1. 在 VS Code 按 `Ctrl+Shift+P`，执行 **Open Eino Dev**。
2. 进入可视化调试页面，配置调试地址：IP `127.0.0.1`，Port `52538`。
3. 连接成功后，在编排产物列表中选择 **report**，查看六个节点及其分支、循环连线。

使用 Remote SSH / WSL 时，插件应安装在运行 Go 进程的远程环境。如果插件运行在本机而进程位于远程服务器，可通过 VS Code 的“端口”面板转发 `52538`，再连接本机的转发端口。

| 环境变量 | 默认值 | 用途 |
| --- | --- | --- |
| `EINO_DEV` | 关闭 | 设为 `1` 启用调试服务 |
| `EINO_DEV_IP` | `127.0.0.1` | 调试服务监听 IP |
| `EINO_DEV_PORT` | `52538` | 调试服务监听端口，范围 1–65535 |
| `REPORT_LISTEN_ADDR` | `127.0.0.1:18080` | 报告 WebSocket 和健康检查地址 |

端口已占用时，调整对应变量，并在插件中填写相同的调试端口。查看拓扑只需要报告进程启动成功；执行节点还需要相应的模型、NL2SQL 服务和模板等资源。

## 导出 Graph Schema 到编排页面

已导出的 [report.graph.json](graphs/report.graph.json) 可在 **Eino Workflow** 编排页面导入：点击 **Import/Export Graph Schema → Import Graph**，复制该 JSON 文件的完整内容粘贴到弹窗中，然后确认。导入已有文件无需运行 Go 服务；完整接入步骤也见[根目录 README](../README.md#63-go-工作流与-vs-code-eino-插件)。

工作流调整后，保持 `EINO_DEV=1` 的报告进程运行，从仓库根目录重新导出：

```bash
python scripts/export_report_graph.py
```

调试服务地址有变化时，传入 `--url http://127.0.0.1:端口`；可用 `--output 路径` 指定文件。脚本从已编译的 `report` 读取真实节点和连线，将调试画布转换为编排页面需要的节点 ID、普通边、条件分支和布局数据。它包含六个业务节点、START/END 和两个条件分支。

此文件用于导入、查看和编辑拓扑。节点中的 Go 函数闭包、依赖注入和分支判断函数不包含在 schema 中，不能据此完整还原业务代码。执行真实工作流时，使用可视化调试页面连接报告进程。编排页面修改 schema 后，也需要手动同步 Go 源码。

## 节点调试

从 START 点击 **Test Run** 时，输入状态对象，例如：

```json
{"Goal":"查询四川省昨天的降雨情况"}
```

省略 `Now` 时使用当前时间；`Emit` 是 WebSocket 回调，不参与 JSON 序列化，插件直接调试时可省略。插件会收集节点输入、输出和耗时。执行完整流程会调用已配置的模型和查询服务。

也可以从 `write_report` 节点开始，用固定查询结果检查展示，无需调用模型和数据库：

```json
{"Meaning":"query","Results":[{"query":"示例查询","result":"用于验证插件的固定结果"}]}
```

## 检查连接

```bash
curl -fsS http://127.0.0.1:52538/eino/devops/ping
curl -fsS http://127.0.0.1:52538/eino/devops/debug/v1/graphs
```

前者应返回 `pong`，后者应包含名为 `report` 的编排产物。若图列表为空，确认开启 `EINO_DEV` 后已重新启动进程，且日志中没有工作流编译错误。
