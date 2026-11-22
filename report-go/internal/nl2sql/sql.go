package nl2sql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	mysql "github.com/go-sql-driver/mysql"
	"vitess.io/vitess/go/vt/sqlparser"
)

const maxRows = 200
const maxToolChars = 5000

var schemaHeader = regexp.MustCompile(`(?m)^# Table: ([A-Za-z0-9_]+)\.([A-Za-z0-9_]+)(?:[,\r\n]|$)`)

// SQLStore 持有 MySQL 连接和 Schema 缓存。模型上下文与表白名单均使用该缓存。
type SQLStore struct {
	DB       *sql.DB
	Database string
	Schema   string
	tables   map[string]string
	parser   *sqlparser.Parser
}

// LoadSQLStore 从 Schema 缓存提取当前数据库的表名。
func LoadSQLStore(db *sql.DB, database, cachePath string) (*SQLStore, error) {
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, fmt.Errorf("read schema cache: %w", err)
	}
	var cache struct {
		Schema string `json:"schema_data"`
	}
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, fmt.Errorf("decode schema cache: %w", err)
	}
	parser, err := sqlparser.New(sqlparser.Options{MySQLServerVersion: "8.0.0"})
	if err != nil {
		return nil, err
	}
	store := &SQLStore{DB: db, Database: database, Schema: cache.Schema,
		tables: make(map[string]string), parser: parser}
	for _, m := range schemaHeader.FindAllStringSubmatch(cache.Schema, -1) {
		if m[1] == database {
			store.tables[m[2]] = m[1] + "." + m[2]
		}
	}
	if len(store.tables) == 0 {
		return nil, errors.New("schema cache contains no tables for the configured MySQL database")
	}
	return store, nil
}

func (s *SQLStore) qualifyTable(name string) (string, error) {
	name = strings.TrimSpace(name)
	if database, table, ok := strings.Cut(name, "."); ok {
		if database != s.Database {
			return "", fmt.Errorf("table is outside the configured database: %q", name)
		}
		name = table
	}
	if full, ok := s.tables[name]; ok {
		return full, nil
	}
	return "", fmt.Errorf("table is absent from the schema cache: %q", name)
}

func (s *SQLStore) qualifyColumn(name string) (string, error) {
	parts := strings.Split(strings.TrimSpace(name), ".")
	if len(parts) != 2 && len(parts) != 3 {
		return "", errors.New("expected table.column or database.table.column")
	}
	column := parts[len(parts)-1]
	table, err := s.qualifyTable(strings.Join(parts[:len(parts)-1], "."))
	if err != nil {
		return "", err
	}
	if !identifier.MatchString(column) {
		return "", errors.New("invalid column identifier")
	}
	return table + "." + column, nil
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
var readableFunctions = strings.Fields("abs acos asin atan ceil ceiling coalesce concat concat_ws cos curdate current_date current_timestamp date datediff date_format day dayofmonth dayofweek dayofyear exp floor greatest hour if ifnull last_day least left length lower lpad ltrim minute mod month now nullif pow power quarter replace right round rpad rtrim second sign sin sqrt substr substring tan timestamp timestampdiff trim truncate ucase upper week year yearweek")

// validate 解析一条只读查询，返回规范化后的 SQL 和实际引用的物理表。
func (s *SQLStore) validate(query string) (string, []string, error) {
	if len(query) == 0 || len(query) > 16000 {
		return "", nil, errors.New("SQL length must be between 1 and 16000 bytes")
	}
	// MySQL interprets some comments as directives. Accept plain SQL only.
	if strings.Contains(query, "/*") || strings.Contains(query, "--") || strings.Contains(query, "#") {
		return "", nil, errors.New("SQL comments are unsupported")
	}
	stmt, err := s.parser.Parse(query)
	if err != nil {
		return "", nil, errors.New("expected one valid MySQL SELECT statement")
	}
	sel, ok := stmt.(sqlparser.SelectStatement)
	if !ok {
		return "", nil, errors.New("only SELECT queries are supported")
	}
	tables := map[string]bool{}
	if err := s.validateSelect(sel, nil, tables); err != nil {
		return "", nil, err
	}
	if len(tables) == 0 {
		return "", nil, errors.New("query must use a table from the schema cache")
	}
	// 在返回上限之外多取一行以判断是否发生截断。调用者指定更小的 LIMIT 时予以保留。
	limit := sel.GetLimit()
	if limit == nil {
		sel.SetLimit(&sqlparser.Limit{Rowcount: sqlparser.NewIntLiteral(strconv.Itoa(maxRows + 1))})
	} else {
		count, ok := limit.Rowcount.(*sqlparser.Literal)
		if !ok || count.Type != sqlparser.IntVal {
			return "", nil, errors.New("LIMIT must be an integer")
		}
		n, err := strconv.ParseUint(count.Val, 10, 64)
		if err != nil {
			return "", nil, errors.New("invalid LIMIT")
		}
		if n > maxRows+1 {
			limit.Rowcount = sqlparser.NewIntLiteral(strconv.Itoa(maxRows + 1))
		}
	}
	return sqlparser.String(sel), slices.Sorted(maps.Keys(tables)), nil
}

func (s *SQLStore) validateSelect(stmt sqlparser.SelectStatement, inherited map[string]bool, tables map[string]bool) error {
	var with *sqlparser.With
	switch root := stmt.(type) {
	case *sqlparser.Select:
		with = root.With
		if root.Into != nil {
			return errors.New("SELECT INTO is unsupported")
		}
	case *sqlparser.Union:
		with = root.With
		if root.Into != nil {
			return errors.New("SELECT INTO is unsupported")
		}
	default:
		return errors.New("unsupported SELECT form")
	}
	if stmt.GetLock() != sqlparser.NoLock {
		return errors.New("locking SELECT is unsupported")
	}
	scope := inherited
	if with != nil {
		if with.Recursive {
			return errors.New("recursive CTEs are unsupported")
		}
		scope = make(map[string]bool)
		maps.Copy(scope, inherited)
		// 先校验 CTE 定义，再将其名称加入作用域。后续 CTE 可以引用先前定义。
		for _, cte := range with.CTEs {
			if err := s.validateSelect(cte.Subquery, scope, tables); err != nil {
				return err
			}
			scope[cte.ID.String()] = true
		}
	}
	return sqlparser.Walk(func(node sqlparser.SQLNode) (bool, error) {
		switch n := node.(type) {
		case *sqlparser.With:
			return false, nil
		case sqlparser.SelectStatement:
			if n != stmt {
				return false, s.validateSelect(n, scope, tables)
			}
		// 这里只检查 FROM 和 JOIN 引用的物理表，派生查询由遍历逻辑继续校验。
		case *sqlparser.AliasedTableExpr:
			table, ok := n.Expr.(sqlparser.TableName)
			if !ok {
				return true, nil
			}
			qualifier, name := table.Qualifier.String(), table.Name.String()
			if qualifier == "" && (scope[name] || name == "dual") {
				return false, nil
			}
			if qualifier != "" {
				name = qualifier + "." + name
			}
			full, err := s.qualifyTable(name)
			if err != nil {
				return false, err
			}
			tables[full] = true
		case *sqlparser.Variable, *sqlparser.AssignmentExpr, *sqlparser.Argument, sqlparser.ListArg:
			return false, errors.New("variables and placeholders are unsupported")
		case *sqlparser.FuncExpr:
			if !n.Qualifier.IsEmpty() || !slices.Contains(readableFunctions, n.Name.Lowered()) {
				return false, fmt.Errorf("unsupported SQL function: %s", n.Name.String())
			}
		}
		return true, nil
	}, stmt)
}

type QueryResult struct {
	Status    string   `json:"status"`
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	RowCount  int      `json:"row_count"`
	Truncated bool     `json:"truncated"`
}

// execute 在只读事务中执行已校验的 SQL，按数据库返回顺序读取列。
func (s *SQLStore) execute(ctx context.Context, validated string) (QueryResult, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return QueryResult{}, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, validated)
	if err != nil {
		return QueryResult{}, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return QueryResult{}, err
	}
	result := QueryResult{Status: "empty", Columns: columns, Rows: [][]any{}}
	for rows.Next() {
		if len(result.Rows) == maxRows {
			result.Truncated = true
			break
		}
		vals := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range vals {
			dest[i] = &vals[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return QueryResult{}, err
		}
		for i, v := range vals {
			// 驱动可能以字节切片返回文本或十进制数。先转换为字符串，避免 JSON 将其编码成 Base64。
			if raw, ok := v.([]byte); ok {
				vals[i] = string(raw)
			}
		}
		result.Rows = append(result.Rows, vals)
	}
	if err := rows.Err(); err != nil {
		return QueryResult{}, err
	}
	result.RowCount = len(result.Rows)
	if result.RowCount > 0 {
		result.Status = "ok"
	}
	return result, nil
}

// Limit the JSON without cutting a row or returning malformed JSON.
func resultMessage(result *QueryResult) (string, error) {
	for {
		result.RowCount = len(result.Rows)
		data, err := json.Marshal(result)
		if err != nil {
			return "", errors.New("query result could not be encoded")
		}
		if len([]rune(string(data))) <= maxToolChars {
			if result.Status == "ok" && len(result.Rows) == 0 {
				return "", errors.New("query rows exceed the tool output limit; select fewer columns")
			}
			return string(data), nil
		}
		if len(result.Rows) == 0 {
			return "", errors.New("query columns exceed the tool output limit")
		}
		result.Truncated = true
		result.Rows = result.Rows[:len(result.Rows)-1]
	}
}

// sqlError 将常见驱动错误转为可用于修正查询的提示，避免暴露数据库原始错误。
func sqlError(err error) string {
	var driverError *mysql.MySQLError
	if errors.As(err, &driverError) {
		switch driverError.Number {
		case 1052:
			return "字段名称有歧义，请使用表别名"
		case 1054:
			return "字段不存在，请对照 schema 检查字段名"
		case 1064:
			return "MySQL 语法错误，请检查查询语句"
		case 1146:
			return "数据表不存在，请对照 schema 检查表名"
		}
		return fmt.Sprintf("MySQL query failed (code %d)", driverError.Number)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "查询超时，请缩小统计范围"
	}
	return "数据库查询失败"
}
