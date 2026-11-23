package transport

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/zhanglt/ReportAgent/report-go/internal/querysession"
	"github.com/zhanglt/ReportAgent/report-go/internal/report"
)

// WebSocket 在每条连接内串行处理请求，并以 JSON 发送各阶段事件。
func WebSocket(workflow *report.Workflow, timeout time.Duration, origins []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: origins})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(1 << 20)

		// 为当前连接生成独立会话标识，普通追问通过请求头传递给 NL2SQL，JSON 请求体保持兼容。
		ctx, cancel := context.WithCancel(querysession.WithID(r.Context(), querysession.NewID()))
		defer cancel()
		incoming := make(chan string, 16)
		go readRequests(ctx, cancel, conn, incoming)

		send := func(event any) error {
			writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return wsjson.Write(writeCtx, conn, event)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case goal := <-incoming:
				if ctx.Err() != nil {
					return
				}
				if goal == "" {
					if send(report.Event{Type: "error", Message: "请输入有效的问题"}) != nil {
						return
					}
					continue
				}
				if send(report.Event{Type: "start", Message: goal}) != nil {
					return
				}
				// 每次请求有独立执行时限。超时后通过仍然有效的连接上下文发送错误事件。
				requestCtx, finish := context.WithTimeout(ctx, timeout)
				final, err := workflow.Run(requestCtx, goal, func(event report.Event) error { return send(event) })
				deadlineExceeded := errors.Is(requestCtx.Err(), context.DeadlineExceeded)
				finish()
				if err != nil {
					if ctx.Err() == nil {
						var stageErr *report.StageError
						stage := ""
						if errors.As(err, &stageErr) {
							stage = stageErr.Stage
						}
						slog.Warn("report failed", "stage", stage, "timeout", deadlineExceeded,
							"stage_timeout", errors.Is(err, context.DeadlineExceeded))
						message := "处理请求时出错: 报告流程执行失败"
						if deadlineExceeded {
							message = "处理请求时出错: 报告流程执行超时"
						}
						if send(report.Event{Type: "error", Message: message}) == nil {
							_ = conn.Close(websocket.StatusNormalClosure, "")
						}
					}
					return
				}
				if send(final) != nil {
					return
				}
			}
		}
	}
}

// Reading during execution lets a disconnect cancel model and NL2SQL calls.
func readRequests(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, incoming chan<- string) {
	defer cancel()
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if kind != websocket.MessageText {
			_ = conn.Close(websocket.StatusUnsupportedData, "text requests required")
			return
		}
		select {
		case incoming <- strings.TrimSpace(string(data)):
		case <-ctx.Done():
			return
		default:
			_ = conn.Close(websocket.StatusPolicyViolation, "too many pending requests")
			return
		}
	}
}
