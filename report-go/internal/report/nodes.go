package report

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/zhanglt/ReportAgent/report-go/internal/querysession"
)

func (w *Workflow) intent(ctx context.Context, s *requestState) error {
	var intent struct {
		Intent string `json:"intent"`
	}
	err := w.complete(ctx, intentSystem, map[string]any{
		"task": "classify_intent", "labels": []string{"query", "report", "other"},
		"fewshots": json.RawMessage(intentExamples), "user_input": s.Goal,
	}, 0, &intent)
	// 模型分类失败且请求仍有效时，使用关键词规则判断意图。
	s.Meaning = strings.ToLower(strings.TrimSpace(intent.Intent))
	if err != nil || (s.Meaning != "query" && s.Meaning != "report" && s.Meaning != "other") {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.Meaning = "query"
		if strongReportIntent.MatchString(s.Goal) || (!queryIntent.MatchString(s.Goal) && weakReportIntent.MatchString(s.Goal)) {
			s.Meaning = "report"
		}
	}
	var details struct {
		ReportType string `json:"report_type"`
		Time       string `json:"time"`
		Region     string `json:"region"`
	}
	if err := w.complete(ctx, extractSystem, map[string]any{
		"task": "extract_report_request_context", "fewshots": json.RawMessage(extractExamples),
		"normalized_input": resolveDates(s.Goal, s.Now),
	}, 0, &details); err != nil {
		return err
	}
	s.ReportType = normalizeReportType(details.ReportType)
	s.Region = strings.TrimSpace(details.Region)
	switch s.Meaning {
	case "query":
		s.Time = normalizeDate(details.Time)
		s.addQuery(resolveDates("请查询："+s.Goal, s.Now))
		s.Outline = append(s.Outline, s.Goal)
	case "report":
		s.Time = normalizeDate(details.Time)
		if s.Region == "" {
			s.Region = "四川省"
		}
	case "other":
		s.Done = true
		s.Outline = append(s.Outline, "暂不支持的请求类型")
	}
	return nil
}

func (s *requestState) addQuery(query string) {
	s.Plan = append(s.Plan, queryTask{Query: query})
	s.Queries = append(s.Queries, query)
}

// schedule 从队首取出一条待执行查询。队列为空时转入写作。
func schedule(_ context.Context, s *requestState) error {
	s.Iterations++
	s.Done = len(s.Plan) == 0
	s.Current = nil
	if !s.Done {
		s.Current = &s.Plan[0]
		s.Plan = s.Plan[1:]
	}
	return nil
}

func (w *Workflow) runQuery(ctx context.Context, s *requestState) error {
	// 报告子查询自带上下文。这里清除会话标识，使报告与同一连接的普通查询历史隔离。
	if s.Meaning == "report" {
		ctx = querysession.WithID(ctx, "")
	}
	output, err := w.query.Query(ctx, s.Current.Query)
	if err != nil {
		return err
	}
	s.Results = append(s.Results, Result{Query: s.Current.Query, Result: output})
	s.LastAttempt = s.Current.Attempt
	s.Current = nil
	return nil
}

func (w *Workflow) review(ctx context.Context, s *requestState) error {
	latest := s.Results[len(s.Results)-1]
	var decision struct {
		Status string `json:"status"`
	}
	err := w.complete(ctx, reviewSystem, map[string]any{
		"task": "review_query_result", "labels": []string{"ok", "empty", "error"},
		"fewshots": json.RawMessage(reviewExamples), "query": latest.Query, "result": latest.Result,
	}, 0, &decision)
	status := strings.ToLower(strings.TrimSpace(decision.Status))
	if err != nil || (status != "ok" && status != "empty" && status != "error") {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		status = classifyResult(latest.Result)
	}
	// 失败和空结果也作为证据保留，便于报告说明数据覆盖缺口。
	s.Evidence = append(s.Evidence, Evidence{
		Index: len(s.Results), Query: latest.Query, Status: status,
		Result: latest.Result, SupportsConclusion: status == "ok",
	})
	switch status {
	case "empty":
		s.Warnings = appendUnique(s.Warnings, fmt.Sprintf("查询“%s”未返回有效数据。", latest.Query))
	case "error":
		s.Errors = appendUnique(s.Errors, fmt.Sprintf("查询“%s”执行异常：%s", latest.Query, summarize(latest.Result)))
		if w.options.RetryQueryErrors && s.LastAttempt == 0 {
			if err := w.scheduleCorrection(ctx, s, latest); err != nil {
				return err
			}
		}
	}
	s.Done = len(s.Plan) == 0
	return nil
}

const correctionSystem = "你负责修订一条自然语言水文查询。根据查询失败原因修订表述，保持原时间范围、地区、对象、指标、单位、筛选和统计口径，不要猜测或补造数据，不要输出 SQL。只输出 JSON 对象，字段 query 为修订后的自然语言查询。无法修订时令 query 为 null。"

// scheduleCorrection 仅将内容不同的修订查询追加到队尾。修订查询带有重试标记，审核时会跳过二次修订。
func (w *Workflow) scheduleCorrection(ctx context.Context, s *requestState, latest Result) error {
	var correction struct {
		Query string `json:"query"`
	}
	if err := w.complete(ctx, correctionSystem, map[string]any{
		"task": "correct_query", "query": latest.Query, "error": capText(latest.Result, 2000, "…"),
	}, 0, &correction); err != nil {
		return ctx.Err()
	}
	query := resolveDates(strings.TrimSpace(correction.Query), s.Now)
	if query == "" || slices.Contains(s.Queries, query) {
		return nil
	}
	s.Plan = append(s.Plan, queryTask{Query: query, Attempt: 1})
	s.Queries = append(s.Queries, query)
	s.Warnings = appendUnique(s.Warnings, fmt.Sprintf("查询“%s”执行失败，已安排一次修订查询。", latest.Query))
	return nil
}

func (w *Workflow) write(ctx context.Context, s *requestState) error {
	switch s.Meaning {
	case "query":
		s.Report = formatResults(s.Results)
	case "report":
		var name, template *string
		if s.Template != nil {
			name = optional(s.Template.Name)
			template = optional(s.Template.Content)
		}
		// 限制写作输入中每条结果的长度，同时保留原始证据供追溯。
		results := make([]map[string]string, 0, len(s.Results))
		for i, r := range s.Results {
			results = append(results, map[string]string{
				"idx": fmt.Sprint(i + 1), "query": capText(r.Query, 1000, "\n...(truncated)"),
				"result": capText(r.Result, w.options.MaxCharsPerResult, "\n...(truncated)"),
			})
		}
		input := fmt.Sprintf("[报告目标]\n%s\n\n[查询任务]\n%s\n\n[写作要求]\n请严格基于查询结果撰写报告，保持结构清晰，缺少证据的地方明确说明。",
			s.Goal, numbered(s.Queries))
		payload, err := json.Marshal(map[string]any{
			"user_input": input, "template_name": name, "report_type": optional(s.ReportType),
			"time": optional(s.Time), "queries": s.Queries, "outline": s.Outline,
			"evidence_summary": s.Evidence, "warnings": s.Warnings, "errors": s.Errors,
			"external_query_results": results, "template_text": template,
		})
		if err != nil {
			return err
		}
		body, err := w.model.Complete(ctx, writeSystem, payload, 0.2, false)
		if err != nil {
			return err
		}
		s.Report = strings.TrimSpace(body)
	default:
		s.Report = "暂不支持该类型请求。当前仅支持直接查询和生成报告。"
		return nil
	}
	if appendix := traceability(s); appendix != "" {
		if s.Report == "" {
			s.Report = appendix
		} else {
			s.Report += "\n\n" + appendix
		}
	}
	return nil
}
