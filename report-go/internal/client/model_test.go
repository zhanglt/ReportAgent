package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudwego/eino/schema"
)

// 检查实际请求体，确保各类模型调用都传递强度，留空时省略该字段。
func TestModelReasoningEffortRequests(t *testing.T) {
	for _, effort := range []string{"", "medium", "high", "xhigh", "max"} {
		for _, mode := range []string{"text", "json", "tools"} {
			for _, streaming := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", effort, mode, streaming), func(t *testing.T) {
					requests := 0
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests++
						var body map[string]json.RawMessage
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Errorf("解析请求失败：%v", err)
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						if effort == "" {
							if _, ok := body["reasoning_effort"]; ok {
								t.Error("未配置强度时应省略 reasoning_effort")
							}
						} else if string(body["reasoning_effort"]) != fmt.Sprintf("%q", effort) {
							t.Errorf("reasoning_effort = %s，期望 %q", body["reasoning_effort"], effort)
						}
						if streaming {
							w.Header().Set("Content-Type", "text/event-stream")
							fmt.Fprint(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
							return
						}
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, `{"id":"test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
					}))
					defer server.Close()
					ctx := context.Background()
					mdl, err := NewModel(ctx, server.Client(), server.URL+"/v1", "test", "test-model", effort, streaming)
					if err != nil {
						t.Fatal(err)
					}
					var content string
					if mode == "tools" {
						var msg *schema.Message
						msg, err = mdl.ToolTurn(ctx, []*schema.Message{schema.UserMessage("测试")}, []*schema.ToolInfo{{Name: "lookup", Desc: "查询"}})
						if msg != nil {
							content = msg.Content
						}
					} else {
						content, err = mdl.Complete(ctx, "测试", []byte("测试"), 0, mode == "json")
					}
					if err != nil {
						t.Fatal(err)
					}
					if content != "ok" || requests != 1 {
						t.Errorf("返回内容=%q，请求次数=%d", content, requests)
					}
				})
			}
		}
	}
}

func TestModelRejectsInvalidReasoningEffort(t *testing.T) {
	if _, err := NewModel(context.Background(), http.DefaultClient, "http://localhost/v1", "test", "test-model", "hgh", false); err == nil {
		t.Fatal("错误的推理强度应在初始化时被拒绝")
	}
}
