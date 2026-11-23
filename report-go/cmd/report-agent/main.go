package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/cloudwego/eino-ext/devops"
	"github.com/joho/godotenv"
	"github.com/zhanglt/ReportAgent/report-go/internal/client"
	"github.com/zhanglt/ReportAgent/report-go/internal/config"
	"github.com/zhanglt/ReportAgent/report-go/internal/report"
	"github.com/zhanglt/ReportAgent/report-go/internal/transport"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}

// run 加载共享配置，将报告流程接入 WebSocket 服务。
func run(ctx context.Context) error {
	root, err := config.ResourceRoot()
	if err != nil {
		return err
	}
	// 根目录的 .env 提供默认配置，启动脚本设置的环境变量优先。
	if err := godotenv.Load(filepath.Join(root, ".env")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("cannot load repository .env: %w", err)
	}
	// Eino Dev 必须在 NewWorkflow 编译图之前初始化，插件才能发现编排产物。
	if config.Enabled("EINO_DEV") {
		port, err := config.PositiveInt("EINO_DEV_PORT", 52538)
		if err != nil {
			return err
		}
		if port > 65535 {
			return fmt.Errorf("EINO_DEV_PORT must be between 1 and 65535")
		}
		ip := config.Setting("EINO_DEV_IP", "127.0.0.1")
		if err := devops.Init(ctx, devops.WithDevServerIP(ip), devops.WithDevServerPort(fmt.Sprint(port))); err != nil {
			return fmt.Errorf("start Eino Dev server: %w", err)
		}
		log.Printf("Eino Dev listening at http://%s (graph: report)", net.JoinHostPort(ip, fmt.Sprint(port)))
	}
	apiKey := strings.TrimSpace(os.Getenv("DASHSCOPE_APIKEY"))
	if apiKey == "" {
		return fmt.Errorf("DASHSCOPE_APIKEY is required")
	}
	baseURL, err := config.Endpoint("BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1")
	if err != nil {
		return err
	}
	queryURL, err := config.Endpoint("NL2SQL_URL", "http://127.0.0.1:8001/nl2sql")
	if err != nil {
		return err
	}
	maxCandidates, err := config.PositiveInt("MAX_TEMPLATE_CANDIDATES", 30)
	if err != nil {
		return err
	}
	maxChars, err := config.PositiveInt("MAX_CHARS_PER_RESULT", 8000)
	if err != nil {
		return err
	}
	timeout, err := config.PositiveInt("REPORT_TIMEOUT_SECONDS", 1200)
	if err != nil {
		return err
	}
	modelTimeout, err := config.PositiveInt("REPORT_MODEL_TIMEOUT_SECONDS", 180)
	if err != nil {
		return err
	}
	// 模型超时约束单次调用，完整报告的时限由 WebSocket 层管理。
	modelClient, err := client.NewModel(ctx, &http.Client{Timeout: time.Duration(modelTimeout) * time.Second},
		baseURL, apiKey, config.Setting("MODEL", "qwen3-max"),
		config.Setting("MODEL_REASONING_EFFORT", ""), config.Enabled("MAIN_LLM_STREAMING"))
	if err != nil {
		return err
	}
	// 客户端超时略长于查询服务的处理时限，为传回结果或错误留出时间。
	queryClient := client.NL2SQL{URL: queryURL, HTTP: &http.Client{Timeout: config.NL2SQLRequestTimeout + 10*time.Second}}
	// 相对模板路径以仓库根目录为准，保证从不同工作目录启动时读取同一套模板。
	templatesDir := config.Setting("TEMPLATES_DIR", "templates")
	if !filepath.IsAbs(templatesDir) {
		templatesDir = filepath.Join(root, templatesDir)
	}
	// 使用模型客户端和 NL2SQL 客户端编译可复用的报告工作流，每次报告请求使用独立状态。
	workflow, err := report.NewWorkflow(ctx, modelClient, queryClient, report.Options{
		// 模板目录用于报告规划，数据库结构文档为查询任务拆分提供字段信息。
		TemplatesDir: templatesDir, SchemaPath: config.SchemaPath(root),
		// 限制参与规划的候选模板数量，以及撰写报告时传给模型的单条查询结果字符数。
		MaxTemplateCandidates: maxCandidates, MaxCharsPerResult: maxChars,
		// 开启后，查询失败时允许重新安排一次查询。
		RetryQueryErrors: config.Enabled("REPORT_RETRY_QUERY_ERRORS"),
	})
	if err != nil {
		return err
	}

	// 健康检查只确认本进程可以接收请求。报告请求通过 WebSocket 执行。
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	origins := strings.FieldsFunc(os.Getenv("REPORT_WS_ORIGINS"), func(r rune) bool { return r == ',' })
	for i, origin := range origins {
		origins[i] = strings.TrimSpace(origin)
	}
	mux.HandleFunc("GET /", transport.WebSocket(workflow, time.Duration(timeout)*time.Second, origins))
	address := config.Setting("REPORT_LISTEN_ADDR", "127.0.0.1:18080")
	// 连接继承服务上下文，进程收到退出信号时可以取消正在执行的报告。
	server := &http.Server{
		Addr: address, Handler: mux, ReadHeaderTimeout: 5 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	log.Printf("report WebSocket listening at ws://%s/", listener.Addr())
	// 退出时使用新的短时上下文关闭 HTTP 监听，避免继承已经取消的信号上下文。
	shutdown := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			deadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := server.Shutdown(deadline); err != nil {
				log.Printf("report server shutdown: %v", err)
			}
		case <-shutdown:
		}
	}()
	defer close(shutdown)
	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
