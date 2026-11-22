# Go 报告工作流

`internal/report/workflow.go` 使用 Eino Graph 编排报告流程，包含六个业务节点、条件分支和查询循环。启动与迁移说明见[重构文档](../docs/eino-refactor-plan.md)。

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
