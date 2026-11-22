package report

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	strongReportIntent = regexp.MustCompile(`生成|撰写|写.{0,8}(报告|快报|周报|月报|简报|通报)|水情(报告|快报|周报|月报)`)
	queryIntent        = regexp.MustCompile(`查询|多少|最大|最小|水位|雨量|流量|涨水|超警|监测站|站点`)
	weakReportIntent   = regexp.MustCompile(`报告|快报|周报|月报|简报|通报`)
	fullDate           = regexp.MustCompile(`(20\d{2})[-/.年](\d{1,2})[-/.月](\d{1,2})日?`)
	yearMonth          = regexp.MustCompile(`(20\d{2})[-/.年](\d{1,2})月?`)
)

func resolveDates(s string, now time.Time) string {
	today := now.Format(time.DateOnly)
	yesterday := now.AddDate(0, 0, -1).Format(time.DateOnly)
	tomorrow := now.AddDate(0, 0, 1).Format(time.DateOnly)
	return strings.NewReplacer(
		"今天到明天", today+"到"+tomorrow, "今天至明天", today+"到"+tomorrow,
		"今天", today, "今日", today, "昨天", yesterday, "昨日", yesterday, "明天", tomorrow,
	).Replace(s)
}

func normalizeDate(s string) string {
	s = strings.TrimSpace(s)
	if m := fullDate.FindStringSubmatch(s); m != nil {
		month, _ := strconv.Atoi(m[2])
		day, _ := strconv.Atoi(m[3])
		return fmt.Sprintf("%s-%02d-%02d", m[1], month, day)
	}
	if m := yearMonth.FindStringSubmatch(s); m != nil {
		month, _ := strconv.Atoi(m[2])
		return fmt.Sprintf("%s-%02d", m[1], month)
	}
	return s
}

func normalizeReportType(s string) string {
	s = strings.TrimSpace(s)
	for _, name := range []string{"日报", "周报", "月报"} {
		if strings.Contains(s, name) {
			return name
		}
	}
	switch strings.ToLower(s) {
	case "daily", "日":
		return "日报"
	case "weekly", "周":
		return "周报"
	case "monthly", "月":
		return "月报"
	}
	return s
}

// capText 按 Unicode 字符数截断，避免在多字节中文字符中间截断。
func capText(s string, limit int, suffix string) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit]) + suffix
}

func summarize(s string) string {
	cleaned := strings.Join(strings.Fields(s), " ")
	if cleaned == "" {
		return "结果为空"
	}
	return capText(cleaned, 160, "...")
}

// classifyResult 在模型审核不可用或返回无效分类时使用本地规则。
func classifyResult(result string) string {
	s := strings.ToLower(strings.TrimSpace(result))
	for _, mark := range []string{"traceback", "exception", "error", "failed", "超时", "失败", "错误", "异常", "http 5", "http 4"} {
		if strings.Contains(s, mark) {
			return "error"
		}
	}
	switch s {
	case "", "[]", "{}", "null", "none", "nan":
		return "empty"
	}
	for _, mark := range []string{"未查询到", "暂无数据", "没有数据", "无结果", "结果为空", "empty result", "no data", "no rows", "0 rows"} {
		if strings.Contains(s, mark) {
			return "empty"
		}
	}
	return "ok"
}

func appendUnique(list []string, s string) []string {
	if s != "" && !slices.Contains(list, s) {
		return append(list, s)
	}
	return list
}

func numbered(items []string) string {
	lines := make([]string, len(items))
	for i, item := range items {
		lines[i] = fmt.Sprintf("%d. %s", i+1, item)
	}
	return strings.Join(lines, "\n")
}

func formatResults(results []Result) string {
	if len(results) == 0 {
		return "NL2SQL 未产生任何结果。"
	}
	blocks := make([]string, len(results))
	for i, result := range results {
		q, r := strings.TrimSpace(result.Query), strings.TrimSpace(result.Result)
		if q == "" {
			q = "（空）"
		}
		if r == "" {
			r = "（无结果）"
		}
		blocks[i] = fmt.Sprintf("## 查询 %d\n问题：%s\n\n结果：\n%s", i+1, q, r)
	}
	return strings.Join(blocks, "\n\n")
}

// traceability 将提纲、查询证据、警告和失败项整理为报告附录。
func traceability(s *requestState) string {
	var lines []string
	if len(s.Outline) > 0 {
		lines = append(lines, "## 报告提纲", numbered(s.Outline))
	}
	if len(s.Evidence) > 0 {
		lines = append(lines, "## 查询证据")
		for _, item := range s.Evidence {
			lines = append(lines, fmt.Sprintf("- [%d] %s | %s", item.Index, item.Status, item.Query))
			if raw := strings.TrimSpace(item.Result); raw != "" {
				lines = append(lines, "  原始结果："+raw)
			}
		}
	}
	for _, section := range []struct {
		title string
		items []string
	}{{"## 风险提示", s.Warnings}, {"## 查询失败", s.Errors}} {
		if len(section.items) > 0 {
			lines = append(lines, section.title)
			for _, item := range section.items {
				lines = append(lines, "- "+item)
			}
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
