package nl2sql

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

type fieldDocsModel struct{ turns int }

func (m *fieldDocsModel) ToolTurn(context.Context, []*schema.Message, []*schema.ToolInfo) (*schema.Message, error) {
	m.turns++
	return schema.AssistantMessage("", []schema.ToolCall{{
		ID: "fields", Type: "function",
		Function: schema.FunctionCall{Name: "retrieve_field_docs", Arguments: `{"table_names":["ST_PPTN_R"]}`},
	}}), nil
}

func TestQueryStopsWhenRetrievalUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	model := &fieldDocsModel{}
	agent := &Agent{
		Model: model, Zone: time.UTC,
		SQL:       &SQLStore{Database: "businessdb", tables: map[string]string{"ST_PPTN_R": "businessdb.ST_PPTN_R"}},
		Retrieval: Retrieval{BaseURL: server.URL, HTTP: server.Client()},
	}
	answer, err := agent.Query(context.Background(), "查询2025年5月15日各站点的降雨量", nil)
	if err != nil || answer.Completed || model.turns != 1 || !strings.Contains(answer.Text, "字段说明检索服务不可用") {
		t.Fatalf("answer=%+v turns=%d err=%v", answer, model.turns, err)
	}
	// 规则检索失败也必须保持未完成状态，不能接受未经规则检查的答复。
	state := toolState{executed: true}
	agent.call(context.Background(), "降雨量", &state, schema.ToolCall{
		Function: schema.FunctionCall{Name: "retrieve_rules_docs", Arguments: `{"query":"降雨量"}`},
	})
	if state.rulesRead || !strings.Contains(state.dependencyFailure, "业务规则检索服务不可用") {
		t.Fatalf("unexpected rule state: %+v", state)
	}
}
