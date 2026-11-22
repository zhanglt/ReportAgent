package report

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

type templateEntry struct {
	Name    string
	Content string
}

var (
	templateHeading = regexp.MustCompile(`(?m)^#[ \t]+(.+)$`)
	sectionHeading  = regexp.MustCompile(`(?m)^##[ \t]+(.+)$`)
	numberedHeading = regexp.MustCompile(`(?m)^([一二三四五六七八九十]+[、.．][ \t]*.+)$`)
	sectionPrefix   = regexp.MustCompile(`^[一二三四五六七八九十]+[、.．]\s*`)
)

// loadTemplates 可从一个 Markdown 文件读取多个模板，每个一级标题开始一个模板。纯文本用文件名作为模板名。
func loadTemplates(dir string) []templateEntry {
	files, _ := os.ReadDir(dir)
	var templates []templateEntry
	for _, ext := range []string{".md", ".txt"} {
		for _, file := range files {
			name := file.Name()
			if file.IsDir() || filepath.Ext(name) != ext || strings.HasPrefix(name, ".") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			body := string(data)
			if ext == ".md" {
				matches := templateHeading.FindAllStringSubmatchIndex(body, -1)
				if len(matches) > 0 {
					for i, match := range matches {
						end := len(body)
						if i+1 < len(matches) {
							end = matches[i+1][0]
						}
						templates = append(templates, templateEntry{
							Name:    strings.TrimSpace(body[match[2]:match[3]]),
							Content: strings.TrimSpace(body[match[1]:end]),
						})
					}
					continue
				}
			}
			templates = append(templates, templateEntry{Name: strings.TrimSuffix(name, ext), Content: body})
		}
	}
	return templates
}

// chooseTemplate 优先选取名称与报告类型完全匹配的模板。交给模型选择时限制候选数和预览长度。
func (w *Workflow) chooseTemplate(ctx context.Context, s *requestState, entries []templateEntry) (*templateEntry, error) {
	if i := slices.IndexFunc(entries, func(e templateEntry) bool { return e.Name == s.ReportType }); i >= 0 {
		return &entries[i], nil
	}
	limit := min(len(entries), w.options.MaxTemplateCandidates)
	candidates := make([]map[string]string, 0, limit)
	for _, e := range entries[:limit] {
		preview := strings.ReplaceAll(strings.TrimSpace(e.Content), "\n", " ")
		candidates = append(candidates, map[string]string{"template_name": e.Name, "preview": capText(preview, 200, "...")})
	}
	var response struct {
		Name string `json:"template_name"`
	}
	err := w.complete(ctx, selectSystem, map[string]any{
		"user_input": s.Goal, "report_type": optional(s.ReportType), "candidates": candidates,
	}, 0.1, &response)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(entries, func(e templateEntry) bool { return e.Name == strings.TrimSpace(response.Name) })
	if i < 0 {
		return nil, nil
	}
	return &entries[i], nil
}

// templateOutline 识别 Markdown 标题及中文编号的章节，并统一常见水情主题的名称。
func templateOutline(body string) []string {
	if strings.TrimSpace(body) == "" {
		return []string{"总体情况"}
	}
	matches := sectionHeading.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		matches = numberedHeading.FindAllStringSubmatch(body, -1)
	}
	if len(matches) == 0 {
		return []string{"正文"}
	}
	outline := make([]string, 0, len(matches))
	for _, match := range matches {
		title := strings.TrimSpace(sectionPrefix.ReplaceAllString(strings.TrimSpace(match[1]), ""))
		for _, term := range []string{"雨情", "水情", "汛情", "旱情", "工情", "水质"} {
			if strings.Contains(title, term) {
				title = term
				break
			}
		}
		if title == "" {
			title = "正文"
		}
		outline = append(outline, title)
	}
	return outline
}

func readSchema(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var schema struct {
		Data string `json:"schema_data"`
	}
	if json.Unmarshal(data, &schema) != nil {
		return ""
	}
	return capText(strings.TrimSpace(schema.Data), 12000, "\n...[schema truncated]...")
}

func (w *Workflow) plan(ctx context.Context, s *requestState) error {
	if s.Meaning != "report" {
		return nil
	}
	templates := loadTemplates(w.options.TemplatesDir)
	if len(templates) == 0 {
		s.Warnings = append(s.Warnings, "未找到任何可用模板。")
	} else {
		var err error
		s.Template, err = w.chooseTemplate(ctx, s, templates)
		if err != nil {
			return err
		}
	}
	var body, name string
	if s.Template != nil {
		body, name = s.Template.Content, s.Template.Name
		if s.ReportType == "" {
			s.ReportType = name
		}
	}
	s.Outline = templateOutline(body)
	s.QueryTasks = len(s.Outline)
	excerpt := readSchema(w.options.SchemaPath)
	// 缺少模板或 Schema 时保留提纲和警告，供写作阶段说明查询计划的缺口。
	if body == "" || excerpt == "" {
		s.Warnings = append(s.Warnings, "模板或 schema 不完整，无法生成查询计划。")
		return nil
	}
	var planned struct {
		Outline []string          `json:"outline"`
		Queries []json.RawMessage `json:"queries"`
		Tasks   []json.RawMessage `json:"query_tasks"`
	}
	if err := w.complete(ctx, planSystem, map[string]any{
		"user_input": s.Goal, "report_type": optional(s.ReportType), "time": optional(s.Time), "region": s.Region,
		"template_name": name, "template_text": body, "template_outline": s.Outline, "schema_excerpt": excerpt,
	}, 0.1, &planned); err != nil {
		return err
	}
	if len(planned.Outline) > 0 {
		s.Outline = planned.Outline
	}
	var taskCount int
	for _, task := range planned.Tasks {
		var fields map[string]json.RawMessage
		if json.Unmarshal(task, &fields) == nil && fields != nil {
			taskCount++
		}
	}
	if taskCount > 0 {
		s.QueryTasks = taskCount
	}
	// 剔除空查询和重复查询，保留模型给出的原始顺序。
	seen := make(map[string]bool, len(planned.Queries))
	for _, item := range planned.Queries {
		var query string
		if json.Unmarshal(item, &query) != nil {
			continue
		}
		query = strings.TrimSpace(query)
		if query == "" || seen[query] {
			continue
		}
		seen[query] = true
		s.addQuery(resolveDates(query, s.Now))
	}
	if len(s.Plan) == 0 {
		s.Warnings = append(s.Warnings, "LLM 未生成有效查询。")
	}
	if len(s.Plan) > 150 {
		return fmt.Errorf("report query plan exceeds 150 queries")
	}
	return nil
}
