# ReportAgent 的 Go 与 Eino 重构计划

首个可独立上线的版本是 **Go/Eino 报告服务 + 现有 Python NL2SQL 服务**。先用同一批请求验证报告编排、前端协议和数据口径，再单独替换查询服务。

[原始架构](architecture.md)定义业务目标。业务人员维护报告模板，技术人员维护系统、字段说明和数据，系统把自然语言需求拆成查询任务，基于真实结果撰写报告。Python 源码用于核对现有接口与兼容行为。

| 原始设计 | 当前 Python 实现与迁移安排 |
| --- | --- |
| 查询执行错误后调整查询，有限次重试 | 当前审核节点记录证据和错误后继续已有队列。Go 默认采用这一行为，开启 `REPORT_RETRY_QUERY_ERRORS` 后可对审核判定为执行错误的任务安排一次修订。原结果仍保存在证据中。空结果与 HTTP 传输错误分别沿用当前路径。 |
| 用 Schema、字段解释、跨表路径与业务规则约束 NL2SQL，执行前校验 SQL | 第一期沿用现有查询服务。校验、白名单和结构化结果属于第二期验收，需按原始设计独立核对。 |
| 私有化模型可通过 OpenAI 兼容接口接入 | Go 版从现有环境变量读取模型地址与名称。私有化网关的工具调用和部署验证随第二期开展。 |

## 1. 以当前执行路径为基线

WebSocket 入口为每条文本消息创建一份状态，由 `_execute_with_progress` 驱动节点并推送进度。仓库还定义了 LangGraph 图，两处路由要对照检查。Go 版只维护一份 Eino 图，WebSocket 和测试都调用同一个编译结果。[执行入口](../graph.py) · [状态定义](../state.py) · [分支判断](../helper.py)

| 现有节点 | Go 迁移要求 |
| --- | --- |
| `intent_analysis` | 保留 `query/report/other` 分类、分类失败后的正则判断，以及时间和地区提取。直接查询生成一条任务，报告缺地区时取四川省。日期换算注入时钟，按部署时区对照“今天”“昨天”和跨月请求。 |
| `template_query_and_split` | 报告按现有规则读取 Markdown、TXT 模板及 schema 摘要，生成提纲、结构化任务和自然语言查询。保留选中模板、去重后的原始顺序和空计划警告。 |
| `scheduler` | 每次进入增加一次 `iterations`，从队首取一条查询。有当前任务进入 NL2SQL，队列为空进入写作。 |
| `nl2sql` | 按 `NL2SQL_URL` 发送查询。从 JSON 的 `output` 取结果，与原问题成对保存后清空当前任务。HTTP 失败、无效 JSON 和缺少结果字段进入流程错误出口。 |
| `result_review` | 审核最新结果，模型判断失败时使用本地规则。记录 `ok/empty/error`、原始结果及是否支持结论。空结果记警告，错误文本记错误，若仍有排队任务则继续调度。当前实现即使收到 `needs_follow_up` 也不会自动补查。 |
| `write_report` | 直接查询格式化已有结果，报告请求将模板、结果及证据送入写作模型，两者均按现有规则追加追溯附录。`other` 返回对应的中文提示。 |

实现时逐项核对节点代码、模板解析和写作输入。报告写作对单条结果的截断按字符计数，Go 实现需覆盖中文，原始证据的保留规则也要独立验证。[节点](../node.py) · [模板规划](../template_planner.py) · [报告写作](../report_writer.py)

## 2. Go 服务结构与接口

- 在 `report-go/` 建立独立 Go module。`cmd/report-agent` 负责启动与配置，`internal/report` 放业务状态、六个节点和图，`internal/transport` 处理 WebSocket，`internal/client` 接模型和 NL2SQL。通过依赖注入替换时钟、模型、HTTP 客户端及事件接收器，支持离线回放。
- 使用 Eino 核心 `v0.9.22`。用 `compose.NewGraph[*requestState, *requestState]` 创建有环图，六个业务节点用 `AddLambdaNode` 注册，在调度和审核处通过 `AddBranch`、`NewGraphBranch` 各选一条出边。以 `AnyPredecessor` 模式编译，启动时检查建图和编译错误。每条请求以独立状态调用 `Invoke`。初始计划最多 150 条查询，图最多执行 1,000 步，可覆盖每条查询修订一次的上限。依赖锁定在 `go.mod` 和 `go.sum`。[Eino 图](https://github.com/cloudwego/eino/tree/v0.9.22/compose) · [编译选项](https://github.com/cloudwego/eino/blob/v0.9.22/compose/graph_compile_options.go)
- WebSocket 用 `net/http` 和 `github.com/coder/websocket`，兼容 `ws://localhost:8080/` 的纯文本输入。同一连接按收到的顺序处理请求。节点包装函数统一发送 `stage`，包含节点名、`started/completed` 和状态摘要。成功序列为 `start → stage… → final`。空消息返回 `error` 后可继续输入，节点异常按现有协议结束本次处理。连接断开应取消 Go 侧正在进行的模型读取和 HTTP 等待。
- `final` 对齐现有字段 `final_report/state/warnings/errors/evidence_summary/outline`。为状态摘要与证据单独定义 JSON 类型，核对计数、空数组、可空字段和中文消息。节点失败时不得发送该节点的 `completed` 或成功的 `final`。[事件定义](../graph.py)
- 报告模型使用 Eino 的 OpenAI 兼容适配器。沿用 `BASE_URL`、`MODEL`、`DASHSCOPE_APIKEY` 和 `MAIN_LLM_STREAMING`。保留各节点提示词、温度及 JSON 输出要求。开启流式模式时拼接首个候选的文本片段，再交给节点解析。NL2SQL 的 `NL2SQL_MODEL` 与 `NL2SQL_STREAMING` 由查询服务独立读取。[模型调用](../helper.py) · [查询服务配置](../NL2SQL/config/settings.py)
- 用 Go 的 `http.Client` 对接 `POST /nl2sql`，请求示例为 `{"query":"查询指定日期的雨量"}`，响应为 `{"output":"查询结果文本"}`。区分服务返回非成功状态与成功响应中的业务错误文本，后者仍进入结果审核。复用根目录模板、`NL2SQL/schema_cache/schema.json` 和现有环境变量，明确仓库资源根目录，覆盖从仓库根目录及 Go 子目录启动的情况。[接口](../NL2SQL/agent_mod.py)

## 3. 实施顺序与交付物

| 阶段 | 执行内容 | 完成条件 |
| --- | --- | --- |
| M0 固化对照样例 | 记录当前工作树版本及相关未提交改动。固定日期、模板、模型回复和查询响应，在 Python 实际 WebSocket 入口回放普通查询、日报、空数据和故障请求。保存脱敏事件序列、调用次数、最终摘要及报告。 | 同一组固定输入可重复产生相同断言。交互脚本、自动化测试和依赖真实服务的用例分别标明。 |
| M1 验证 Eino 与网关 | 建立 Go module、配置读取和六个最小节点，走通一次单任务和一次多任务回环。通过本地模拟服务检查请求路径、结构化输出及流式拼接，再用部署网关做一次最小调用。 | 图可编译，分支不会重复执行，节点按预期结束。适配器在现有网关能返回可解析 JSON 和普通正文，锁定依赖版本。 |
| M2 迁移报告业务 | 依次迁移意图、日期、模板规划、队列、NL2SQL 客户端、审核和写作。保留 `TEMPLATES_DIR`、`MAX_TEMPLATE_CANDIDATES`、`MAX_CHARS_PER_RESULT` 及 schema 摘要截断规则。将回退限制在 Python 对应的异常处理位置。 | 通过下节的离线对照。任务顺序、调用次数、状态摘要、证据归属及附录均能追溯至输入样例。 |
| M3 接入并联调 | 新增可配置监听地址，在本地备用端口（如 `18080`）启动 Go；保持 Python 对照入口可用。先接模拟 NL2SQL，再接真实配置，用同一客户端分别发出请求并采集到终止事件。 | 直接查询与一份完整日报均有验收记录，注明数据日期、来源、站点覆盖和截断情况。真实模型下对比事实与结构。 |
| M4 切换与回滚 | 先将测试客户端指向 Go，再切换正式 WebSocket 入口。切换时让进行中的报告完成，记录各节点耗时、查询次数、警告数、终止事件及原因。记录 Python 启动命令和原入口映射。 | 完成一次切换与回切演练。遇到协议差异、重复查询、报告数值失真或无法到达终止事件，可把新连接切回 Python。 |

## 4. 验收用例

| 场景 | 必须验证的结果 |
| --- | --- |
| 普通查询、其他意图、空消息 | 查询仅执行一次且跳过报告写作模型。其他意图返回现有提示。空消息产生错误事件，同连接下一条有效请求可完成。 |
| 多章节四川水情日报 | 固定时钟和模板，检查日期、默认地区、提纲、任务去重与顺序。计划有 N 条查询就收到 N 次查询调用，最后一条审核后直接写作。 |
| 空结果、错误文本、传输错误 | 前两者保留证据并继续剩余任务。HTTP 500、非法 JSON 或缺少 `output` 产生终止错误。检查两种错误路径的事件和调用次数。 |
| 模型和模板边界 | 覆盖意图或审核 JSON 损坏时的本地回退、无模板、空查询计划、写作调用失败。用含空片段和多段中文的流式响应测试拼接，验证非流式请求也能走通。 |
| 报告事实与完整性 | 固定查询与模型回复时比对事件和正文。真实模型只核对站点、时间范围、单位、数字、来源和缺测说明。模拟数据需在验收记录中标明；只返回部分站点时，不能据此认定全域覆盖。 |
| 隔离与负载 | 两个连接并行、同连接连续请求、长任务队列、客户端断开均有用例。检查 Go 报告状态隔离、步数上限和取消传播。Python NL2SQL 使用进程级历史，双跑对比时给两组请求使用相同起始历史或录制响应，避免重复请求污染对照。 |

实施后的例行命令为仓库根目录执行 `python -m unittest discover -s test -p 'test_*.py'`，Go module 内执行 `go test ./...`、`go test -race ./...` 和 `go build ./cmd/report-agent`。联调需另行保存 WebSocket 事件及报告，并用查询结果核对关键数值。`GET /health` 仅确认 NL2SQL 的 HTTP 入口可达。[现有测试](../test) · [健康检查实现](../NL2SQL/agent_mod.py)

## 5. 第二期迁移 NL2SQL

查询侧以独立服务交付。第二期的业务数据库范围为 MySQL 8。沿用原有 HTTP 契约，Go 报告服务通过 `NL2SQL_URL` 选择后端。

1. **建工具基线。** 固定 MySQL 8 业务库快照与查询日期，验证单表、跨表、空集、拒绝写入以及 5,000 字符的工具输出截断，比较数据值和单位。[回归用例](../report-go/internal/nl2sql/regression_test.go)
2. **迁移结构与检索。** 先从相同 schema 缓存读取表结构，迁移数据库驱动、标识符限定和 Neo4j 最短路径查询。保留可复用的 Python BGE/Chroma 检索作为过渡服务，在索引副本上核对表名过滤、字段文档前三条、规则检索前五条及实际调用的置信度阈值 0.5。记录模型版本、向量维度和距离算法后再替换嵌入运行时。[检索实现](../NL2SQL/rag/retriever.py) · [关系查询](../NL2SQL/knowledge_graph/graph_query.py)
3. **接入 Eino Agent。** 将四个工具接入 Eino 工具调用循环，保留 schema、业务规则、时区提示和多轮历史。先用当前网关验证工具调用与流式输出，再回放完整问答。现有接口无会话标识，迁移测试需明确历史的生命周期；如引入会话隔离，单独设计向后兼容的协议及跨用户测试。[历史与工具调度](../NL2SQL/agent.py)
4. **迁移索引作业并切换。** 用测试库和独立目录演练 schema 导出、字段与规则索引构建、图谱重建及恢复。逐项核对表与关系覆盖、检索命中和 SQL 结果，再运行 M3 的整份日报。分别切换报告入口和 NL2SQL 地址，保留可恢复的索引与原服务。图谱重建会清空目标图，演练必须指向专用库。[索引构建](../NL2SQL/rag/embedder.py) · [图谱构建](../NL2SQL/knowledge_graph/graph_builder.py)

## 6. 启动第一期开发版

在仓库根目录启动 Python 查询服务。已有服务时，可以通过 `NL2SQL_URL` 指向它的查询接口。

```bash
python -m uvicorn NL2SQL.agent_mod:app --host 127.0.0.1 --port 8001
```

在另一终端进入 `report-go/`，使用 Go 1.24 或更高版本启动。

```bash
cd report-go
go run ./cmd/report-agent
```

WebSocket 地址为 `ws://127.0.0.1:18080/`，可用 `curl http://127.0.0.1:18080/health` 查看进程是否就绪。程序加载仓库根目录的 `.env`，已导出的环境变量优先。可用 `REPORT_LISTEN_ADDR` 调整监听地址，或用 `REPORT_REPO_ROOT` 指定资源根目录。浏览器前端跨域连接时，用 `REPORT_WS_ORIGINS` 列出允许的来源，例如 `http://localhost:5173`。单份报告默认最多等待 1,200 秒，可用 `REPORT_TIMEOUT_SECONDS` 调整。

在 VS Code Eino Dev 插件中查看实际编排拓扑时，使用 `EINO_DEV=1 go run ./cmd/report-agent` 启动，连接 `127.0.0.1:52538` 并选择 `report`。完整步骤、调试输入和端口配置见 [Go 工作流使用说明](../report-go/README.md)。

若要在开发环境验证原始架构中的有限修订，启动时设定 `REPORT_RETRY_QUERY_ERRORS=1`。当审核判为执行错误且模型返回一条新的自然语言查询时，流程将其追加到队尾，最多修订一次，并同时保留原始和修订结果。用同一日期、地区及指标核对修订前后的查询口径。

## 7. 首轮验证记录

用固定模型回复和 NL2SQL 响应，在备用端口回放了空消息、闲聊、普通查询、多任务日报、查询错误文本和 NL2SQL HTTP 错误。两套服务的事件顺序、状态字段与报告一致。Go 的终止错误消息隐藏上游响应细节，日志保留出错节点。

Go 的竞态检测构建通过相同回放，并验证了模拟流式输出和开启后的修订查询。现有 Python 自动化测试通过 11 项。真实模型网关用普通意图识别请求验证了流式接口及 WebSocket 的 `final` 事件。待验证事项为连接真实业务库生成完整日报、核对站点覆盖和统计值，以及入口切换与回切。

## 8. 启动第二期开发版

Go 的 NL2SQL 服务提供 `POST /nl2sql` 和 `GET /health`。Eino 模型适配器负责原生工具调用，查询服务限制调用轮数，核验字段说明、多表关联路径和业务规则的调用顺序。SQL 执行支持当前业务库的 MySQL 8 方言，以 schema 缓存中的表做白名单，通过 AST 校验、只读事务、20 秒查询超时和 200 行返回上限约束执行。字段说明和业务规则检索使用本机 Python 服务，复用现有 BGE 权重和 Chroma 索引。

从仓库根目录启动检索服务。环境中的 `HF_MODEL_DIR`、`CHROMA_DB_DIR` 和 `RULES_CHROMA_DB_DIR` 指向待使用的本地资源。

```bash
python -m uvicorn NL2SQL.rag.service:app --host 127.0.0.1 --port 18082
```

新终端从仓库根目录生成与 `DB_URI`、`DB_SCHEMA` 一致的缓存。使用独立目标路径可以保留已有缓存。

```bash
python -m NL2SQL.get_schema_cache --output /tmp/reportagent-schema.json
cd report-go
NL2SQL_SCHEMA_CACHE=/tmp/reportagent-schema.json NL2SQL_STREAMING=1 go run ./cmd/nl2sql
```

Go 查询服务默认监听 `127.0.0.1:18081`，可用 `NL2SQL_GO_LISTEN_ADDR` 修改。它读取现有模型和 Neo4j 配置。本机检索地址可通过 `NL2SQL_RETRIEVAL_URL` 调整。`GET /health` 确认进程已启动。业务联调用 `POST /nl2sql` 发送 `{"query":"查询指定日期的最高累计雨量及站点"}`，检查 `output` 和工具日志。

从 `report-go/` 启动报告服务时，用相同的 `NL2SQL_SCHEMA_CACHE` 并设置 `NL2SQL_URL=http://127.0.0.1:18081/nl2sql`。回切时恢复 Python 查询服务的地址。

现有模型网关已通过流式工具调用验证。2026 年 5 月 31 日的跨表测试依次调用字段解释、Neo4j 路径、MySQL 查询和业务规则检索，查得模拟站点 `SC000042` 的累计雨量为 3.375 mm，与独立 SQL 一致。

### 多轮查询与规则索引

直接调用 NL2SQL 时，可通过 `X-NL2SQL-Session` 请求头携带一个随机的 32 位小写十六进制标识。客户端可以用 `openssl rand -hex 16` 生成标识，同一会话的请求重用该值。会话标识按访问凭据处理。未传该请求头时，请求独立执行。Go 报告服务为同一 WebSocket 连接内的普通查询自动传递会话标识，报告的各数据查询独立执行。会话在进程内保留最近五轮成功结果，同一会话的请求串行执行，空闲 30 分钟后失效。

可以从现有 `knowledge` 表构建独立的规则索引。将目标目录传给检索服务即可使用该版本。

```bash
python -m NL2SQL.rag.embedder --rules-output /tmp/reportagent-rules-v2
RULES_CHROMA_DB_DIR=/tmp/reportagent-rules-v2 python -m uvicorn NL2SQL.rag.service:app --host 127.0.0.1 --port 18082
```

单次 NL2SQL 模型调用默认最多等待 120 秒，可用 `NL2SQL_MODEL_TIMEOUT_SECONDS` 调整。模型回合超时且请求仍在有效期内时，重试该回合一次。单次 NL2SQL 请求上限为六分钟。

### 第二期联调记录

更新后的 schema 缓存包含 `businessdb` 的 9 张表。先用固定模型响应验证了同一 HTTP 会话和 WebSocket 连接能承接追问、新连接历史独立，以及旧请求体仍能使用。从 `knowledge` 表构建的独立索引包含一条业务规则，使用规则原文发起检索时命中。

对同一份 2026 年 5 月 31 日四川模拟水情日报发起两次联调。首次在第 5 条查询的模型响应阶段触及原 60 秒超时。调整超时后回放到 `final`，8 条证据均为 `ok`。独立 SQL 核对了 21 个市州、42 个站点、最高日降水量 3.375 mm，以及降水和河道水情各 42 条、预报 126 条。降水和河道水情每站仅有 00:00 一条记录，报告明确说明了日内数据覆盖限制。

## 9. 第二期构建、切换与回滚

在仓库根目录运行。为每个版本选择一个新目录，构建时继续使用业务库和已有本地 BGE 模型。

```bash
mkdir -p data
release=$(mktemp -d "$PWD/data/reportagent-phase2.XXXXXX")
python -m NL2SQL.get_schema_cache --output "$release/schema.json"
python -m NL2SQL.rag.embedder --fields-output "$release/fields"
python -m NL2SQL.rag.embedder --rules-output "$release/rules"
```

如需更新图谱，为该版本准备专用、空的 Neo4j 实例。将 `NEO4J_URI` 指向该实例并使用 `--require-empty`。在确认节点个数、关系个数和关键连接路径后，对新版本做离线 dump，再将 dump 加载到另一个空实例进行恢复验证。

```bash
NEO4J_URI=bolt://127.0.0.1:17687 NEO4J_DATABASE=neo4j \
  python -m NL2SQL.knowledge_graph.graph_builder --schema-cache "$release/schema.json" --require-empty
```

Go 版用本机 `8080` 提供 WebSocket，`8001` 提供 NL2SQL，`18082` 提供 Python 检索。用目标地址和版本目录启动。启动脚本在后台子服务退出时收尾，适合在终端或进程管理器下运行。

```bash
CHROMA_DB_DIR="$release/fields" RULES_CHROMA_DB_DIR="$release/rules" \
  NL2SQL_SCHEMA_CACHE="$release/schema.json" scripts/run_report_stack.sh go
```

回切时先停止当前启动脚本，等待监听端口释放，再用 `scripts/run_report_stack.sh python` 在原端口启动两项 Python 服务。用已知查询确认 `final` 事件和数值后，再用 `go` 模式恢复。要回切索引版本，将上述两个索引目录改回上一个已验证版本后重启检索进程。图谱回切通过 `NEO4J_URI` 指向此前已验证的实例。

### 验收结果

- `go test -race ./...` 与 `go vet ./...` 通过。固定回归用例覆盖 MySQL 聚合、CTE 和关联查询、空结果、截断、写入与非白名单表拒绝，以及会话隔离。Python 现有 11 项测试通过。
- 字段索引包含 7 条文档，规则索引包含 1 条规则，使用原有本地 `bge-small-zh`、Chroma L2 距离和 0.5 阈值。旧版和新版均通过检索加载验证。测试的自然语言水位流量查询在新版命中 1 条，在旧版规则库未命中。当前知识库规模为一条规则，排序比较仅覆盖本次数据集。
- 隔离图谱重建后，通过 dump 和独立实例恢复复核了 9 个表、118 个字段节点、14 条有向外键关系，以及雨量表到测站表的关联路径。
- 本机 `8080` 和 `8001` 完成 Go → Python → Go 回切。用相同问题发起 WebSocket 查询，每次都收到 `final`，站点 `SC000042` 的日降水量均为 3.375 mm。当前本机入口运行 Go 版，业务验证数据来自模拟库。
