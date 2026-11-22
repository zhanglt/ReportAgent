package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/joho/godotenv"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/zhanglt/ReportAgent/report-go/internal/client"
	"github.com/zhanglt/ReportAgent/report-go/internal/config"
	"github.com/zhanglt/ReportAgent/report-go/internal/nl2sql"
	"github.com/zhanglt/ReportAgent/report-go/internal/querysession"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	// Load repository defaults while keeping environment variables set by the launcher.
	root, err := config.ResourceRoot()
	if err != nil {
		return err
	}
	if err := godotenv.Load(filepath.Join(root, ".env")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errors.New("cannot load repository .env")
	}
	db, database, err := openMySQL(os.Getenv("DB_URI"), os.Getenv("DB_SCHEMA"))
	if err != nil {
		return err
	}
	defer db.Close()
	// The configured database and schema cache must agree before the service accepts queries.
	store, err := nl2sql.LoadSQLStore(db, database, config.SchemaPath(root))
	if err != nil {
		return err
	}
	if err := ping(ctx, db); err != nil {
		return errors.New("MySQL connection failed")
	}
	// The graph driver is configured separately from MySQL and used by the join-path tool.
	graph := nl2sql.Graph{Database: strings.TrimSpace(os.Getenv("NEO4J_DATABASE"))}
	if uri := strings.TrimSpace(os.Getenv("NEO4J_URI")); uri != "" {
		graph.Driver, err = neo4j.NewDriverWithContext(uri, neo4j.BasicAuth(os.Getenv("NEO4J_USER"), os.Getenv("NEO4J_PASSWORD"), ""))
		if err != nil {
			return errors.New("invalid Neo4j connection settings")
		}
		defer graph.Driver.Close(context.Background())
	}
	modelURL, err := config.Endpoint("BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1")
	if err != nil {
		return err
	}
	retrievalURL, err := config.Endpoint("NL2SQL_RETRIEVAL_URL", "http://127.0.0.1:18082")
	if err != nil {
		return err
	}
	key := strings.TrimSpace(os.Getenv("DASHSCOPE_APIKEY"))
	if key == "" {
		return errors.New("DASHSCOPE_APIKEY is required")
	}
	// The model timeout applies to each upstream call. The handler sets a separate request deadline.
	modelTimeout, err := config.PositiveInt("NL2SQL_MODEL_TIMEOUT_SECONDS", 120)
	if err != nil {
		return err
	}
	mdl, err := client.NewModel(ctx, &http.Client{Timeout: time.Duration(modelTimeout) * time.Second}, modelURL, key,
		config.Setting("NL2SQL_MODEL", "qwen3-max"), config.Enabled("NL2SQL_STREAMING"))
	if err != nil {
		return err
	}
	zone, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return err
	}
	agent := &nl2sql.Agent{Model: mdl, SQL: store, Graph: graph, Zone: zone,
		Retrieval: nl2sql.Retrieval{BaseURL: retrievalURL, HTTP: &http.Client{Timeout: 20 * time.Second}}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	path := config.Setting("NL2SQL_PATH", "/nl2sql")
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, " \t{}?#") {
		return errors.New("NL2SQL_PATH must be an HTTP path")
	}
	mux.HandleFunc("POST "+path, handleQuery(agent))
	addr := config.Setting("NL2SQL_GO_LISTEN_ADDR", "127.0.0.1:18081")
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx }}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log.Printf("NL2SQL listening at http://%s%s", listener.Addr(), path)
	// Use a fresh deadline for server shutdown because the signal context is already canceled.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(stop)
		case <-done:
		}
	}()
	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func handleQuery(agent *nl2sql.Agent) http.HandlerFunc {
	// Keep one history store per handler. Requests without a session header remain stateless.
	history := nl2sql.NewHistory()
	return func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Query *string `json:"query"`
		}
		// Bound the HTTP body before decoding and require exactly one JSON value.
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		if err := decoder.Decode(&input); err != nil || input.Query == nil || decoder.Decode(new(any)) != io.EOF {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": "query must be a JSON string field"})
			return
		}
		question := strings.TrimSpace(*input.Query)
		if question == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"detail": "query is empty"})
			return
		}
		if len([]rune(question)) > 16000 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"detail": "query is too long"})
			return
		}
		id := r.Header.Get(querysession.Header)
		if id != "" && !querysession.ValidID(id) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"detail": "invalid session identifier"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), config.NL2SQLRequestTimeout)
		defer cancel()
		result, err := history.Run(ctx, id, func(turns []nl2sql.Exchange) (nl2sql.Answer, error) {
			return agent.Query(ctx, question, turns)
		}, question)
		if errors.Is(err, nl2sql.ErrTooManySessions) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"detail": "session capacity reached"})
			return
		}
		if err != nil {
			// Keep provider payloads, SQL data and credentials out of responses and logs.
			log.Printf("NL2SQL request failed (deadline=%t)", errors.Is(ctx.Err(), context.DeadlineExceeded))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": "NL2SQL request failed"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"output": result.Text})
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// openMySQL accepts the Python-compatible URI. An explicit DB_SCHEMA takes precedence.
func openMySQL(uri, schema string) (*sql.DB, string, error) {
	u, err := url.Parse(uri)
	if err != nil || u.Hostname() == "" || u.User == nil {
		return nil, "", errors.New("DB_URI requires a MySQL URL with user and host")
	}
	if u.Scheme != "mysql" && u.Scheme != "mysql+pymysql" {
		return nil, "", errors.New("Go NL2SQL supports mysql:// and mysql+pymysql:// DB_URI")
	}
	database := strings.TrimSpace(schema)
	if database == "" {
		database = strings.TrimPrefix(u.Path, "/")
	}
	if database == "" {
		return nil, "", errors.New("MySQL database name is required")
	}
	port := u.Port()
	if port == "" {
		port = "3306"
	}
	options, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, "", errors.New("invalid DB_URI options")
	}
	for key := range options {
		if key != "charset" && key != "tls" {
			return nil, "", fmt.Errorf("unsupported DB_URI option %q", key)
		}
	}
	cfg := mysql.NewConfig()
	cfg.User = u.User.Username()
	cfg.Passwd, _ = u.User.Password()
	cfg.Net, cfg.Addr, cfg.DBName = "tcp", net.JoinHostPort(u.Hostname(), port), database
	cfg.ParseTime = true
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 30*time.Second, 25*time.Second
	cfg.TLSConfig = options.Get("tls")
	charset := options.Get("charset")
	if charset == "" {
		charset = "utf8mb4"
	}
	if strings.ContainsAny(charset, " \t\n;'\x22") {
		return nil, "", errors.New("invalid DB_URI charset")
	}
	if err := cfg.Apply(mysql.Charset(charset, "")); err != nil {
		return nil, "", errors.New("invalid DB_URI charset")
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, "", errors.New("invalid DB_URI driver settings")
	}
	// Opening the pool is lazy. The startup ping verifies connectivity before serving.
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(8)
	db.SetConnMaxLifetime(10 * time.Minute)
	return db, database, nil
}

func ping(ctx context.Context, db *sql.DB) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return db.PingContext(ctx)
}
