// report 包使用 Eino 编排水情报告的规划、查询和撰写流程。
package report

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/compose"
)

type Model interface {
	Complete(ctx context.Context, system string, input []byte, temperature float32, structured bool) (string, error)
}

type QueryClient interface {
	Query(ctx context.Context, question string) (string, error)
}

type Options struct {
	TemplatesDir          string
	SchemaPath            string
	MaxTemplateCandidates int
	MaxCharsPerResult     int
	RetryQueryErrors      bool
	Now                   func() time.Time
}

type Result struct {
	Query  string `json:"query"`
	Result string `json:"result"`
}

// Evidence 保存查询及原始结果，供读者追溯报告结论。
type Evidence struct {
	Index              int    `json:"index"`
	Query              string `json:"query"`
	Status             string `json:"status"`
	Result             string `json:"result"`
	SupportsConclusion bool   `json:"supports_conclusion"`
}

// Summary 是 WebSocket 进度快照，只传递状态与计数。
type Summary struct {
	Iterations       int     `json:"iterations"`
	Meaning          *string `json:"meaning"`
	TemplateName     *string `json:"template_name"`
	ReportType       *string `json:"report_type"`
	Time             *string `json:"time"`
	Region           *string `json:"region"`
	QueryTasksCount  int     `json:"query_tasks_count"`
	CurrentQuery     *string `json:"current_query"`
	RemainingQueries int     `json:"remaining_queries"`
	ResultsCount     int     `json:"results_count"`
	WarningsCount    int     `json:"warnings_count"`
	ErrorsCount      int     `json:"errors_count"`
	Done             bool    `json:"done"`
}

type Event struct {
	Type    string   `json:"type"`
	Message string   `json:"message,omitempty"`
	Stage   string   `json:"stage,omitempty"`
	Status  string   `json:"status,omitempty"`
	State   *Summary `json:"state,omitempty"`
}

// Final 返回报告正文和证据，保留无数据和失败的查询。
type Final struct {
	Type     string     `json:"type"`
	Report   string     `json:"final_report"`
	State    Summary    `json:"state"`
	Warnings []string   `json:"warnings"`
	Errors   []string   `json:"errors"`
	Evidence []Evidence `json:"evidence_summary"`
	Outline  []string   `json:"outline"`
}

type queryTask struct {
	Query   string
	Attempt int
}

// requestState 只在单次请求内使用。Plan 保存待执行队列，Queries 按顺序记录已安排的查询，供报告附录引用。
type requestState struct {
	Goal        string
	Now         time.Time
	Meaning     string
	ReportType  string
	Time        string
	Region      string
	Template    *templateEntry
	QueryTasks  int
	Plan        []queryTask
	Queries     []string
	Current     *queryTask
	LastAttempt int
	Iterations  int
	Done        bool
	Results     []Result
	Evidence    []Evidence
	Warnings    []string
	Errors      []string
	Outline     []string
	Report      string
	Emit        func(Event) error `json:"-"` // 连接回调不属于可序列化的节点状态。
}

// optional 将空字符串转换为 nil，使 WebSocket 协议中的对应字段仍返回 null。
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *requestState) emit(event Event) error {
	// 插件直接调用节点时没有 WebSocket 回调，节点结果由 Eino Dev 收集。
	if s.Emit == nil {
		return nil
	}
	return s.Emit(event)
}

func (s *requestState) summary() Summary {
	out := Summary{
		Iterations: s.Iterations, Meaning: optional(s.Meaning), ReportType: optional(s.ReportType),
		Time: optional(s.Time), Region: optional(s.Region), QueryTasksCount: s.QueryTasks,
		RemainingQueries: len(s.Plan), ResultsCount: len(s.Results), WarningsCount: len(s.Warnings),
		ErrorsCount: len(s.Errors), Done: s.Done,
	}
	if s.Current != nil {
		out.CurrentQuery = &s.Current.Query
	}
	if s.Template != nil {
		out.TemplateName = optional(s.Template.Name)
	}
	return out
}

type Workflow struct {
	model   Model
	query   QueryClient
	options Options
	graph   compose.Runnable[*requestState, *requestState]
}

// StageError 携带失败节点的名称，供服务端日志定位。
type StageError struct {
	Stage string
	Err   error
}

func (e *StageError) Error() string { return e.Stage + ": " + e.Err.Error() }
func (e *StageError) Unwrap() error { return e.Err }

const (
	intentStage    = "intent_analysis"
	planStage      = "template_query_and_split"
	schedulerStage = "scheduler"
	queryStage     = "nl2sql"
	reviewStage    = "result_review"
	writeStage     = "write_report"
)

// NewWorkflow 编译可复用的 Eino 图，每次调用传入独立的可变状态。
func NewWorkflow(ctx context.Context, model Model, query QueryClient, options Options) (*Workflow, error) {
	if options.Now == nil {
		options.Now = time.Now
	}
	w := &Workflow{model: model, query: query, options: options}
	g := compose.NewGraph[*requestState, *requestState]()
	nodes := []struct {
		name string
		run  func(context.Context, *requestState) error
	}{
		{intentStage, w.intent}, {planStage, w.plan}, {schedulerStage, schedule},
		{queryStage, w.runQuery}, {reviewStage, w.review}, {writeStage, w.write},
	}
	// 调用方提供回调时，每个节点在开始和成功完成时各发送一次进度快照。
	for _, node := range nodes {
		run := func(ctx context.Context, s *requestState) (*requestState, error) {
			if s == nil {
				return nil, errors.New("report state is required")
			}
			if s.Now.IsZero() {
				s.Now = w.options.Now()
			}
			before := s.summary()
			if err := s.emit(Event{Type: "stage", Stage: node.name, Status: "started", State: &before}); err != nil {
				return nil, err
			}
			if err := node.run(ctx, s); err != nil {
				return nil, &StageError{Stage: node.name, Err: err}
			}
			after := s.summary()
			return s, s.emit(Event{Type: "stage", Stage: node.name, Status: "completed", State: &after})
		}
		if err := g.AddLambdaNode(node.name, compose.InvokableLambda(run), compose.WithNodeName(node.name)); err != nil {
			return nil, err
		}
	}
	edges := [][2]string{
		{compose.START, intentStage}, {intentStage, planStage}, {planStage, schedulerStage},
		{queryStage, reviewStage}, {writeStage, compose.END},
	}
	for _, edge := range edges {
		if err := g.AddEdge(edge[0], edge[1]); err != nil {
			return nil, err
		}
	}
	// 调度和审核节点在没有待执行查询时转入报告写作。
	for _, branch := range []struct {
		from string
		next string
		when func(*requestState) bool
	}{
		{schedulerStage, queryStage, func(s *requestState) bool { return s.Current != nil }},
		{reviewStage, schedulerStage, func(s *requestState) bool { return len(s.Plan) > 0 }},
	} {
		choose := func(_ context.Context, s *requestState) (string, error) {
			if branch.when(s) {
				return branch.next, nil
			}
			return writeStage, nil
		}
		if err := g.AddBranch(branch.from, compose.NewGraphBranch(choose, map[string]bool{
			branch.next: true, writeStage: true,
		})); err != nil {
			return nil, err
		}
	}
	var err error
	// 调度或审核任一分支均可进入写作节点。最大步数限制调度循环。
	w.graph, err = g.Compile(ctx, compose.WithGraphName("report"), compose.WithNodeTriggerMode(compose.AnyPredecessor), compose.WithMaxRunSteps(1000))
	if err != nil {
		return nil, fmt.Errorf("compile report graph: %w", err)
	}
	return w, nil
}

// Run 为一次请求固定基准时间，统一换算“今天”等相对日期。
func (w *Workflow) Run(ctx context.Context, goal string, emit func(Event) error) (Final, error) {
	s := &requestState{
		Goal: strings.TrimSpace(goal), Now: w.options.Now(), Emit: emit,
		Warnings: []string{}, Errors: []string{}, Evidence: []Evidence{}, Outline: []string{}, Queries: []string{},
	}
	if s.Goal == "" {
		return Final{}, errors.New("请输入有效的问题")
	}
	s, err := w.graph.Invoke(ctx, s)
	if err != nil {
		return Final{}, err
	}
	return Final{
		Type: "final", Report: s.Report, State: s.summary(), Warnings: s.Warnings,
		Errors: s.Errors, Evidence: s.Evidence, Outline: s.Outline,
	}, nil
}

func (w *Workflow) complete(ctx context.Context, system string, payload map[string]any, temperature float32, out any) error {
	input, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	// 流式片段在模型客户端拼接。这里对完整的模型响应做 JSON 解析。
	content, err := w.model.Complete(ctx, system, input, temperature, true)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(strings.TrimSpace(content), "{") {
		return errors.New("expected a JSON object from model")
	}
	if err := json.Unmarshal([]byte(content), out); err != nil {
		return fmt.Errorf("invalid structured model response: %w", err)
	}
	return nil
}
