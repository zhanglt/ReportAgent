package nl2sql

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMySQLReportQueries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema.json")
	cache := `{"schema_data":"# Table: businessdb.ST_PPTN_R\n# Table: businessdb.ST_STBPRP_B\n"}`
	if err := os.WriteFile(path, []byte(cache), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := LoadSQLStore(nil, "businessdb", path)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		query  string
		tables []string
		limit  string
	}{
		{"aggregation", "SELECT MAX(DYP) FROM ST_PPTN_R WHERE TM >= '2026-05-31'", []string{"businessdb.ST_PPTN_R"}, "limit 201"},
		{"join and CTE", "WITH rainfall AS (SELECT STCD, SUM(DRP) AS mm FROM ST_PPTN_R GROUP BY STCD) SELECT s.STNM, r.mm FROM rainfall r JOIN ST_STBPRP_B s ON s.STCD=r.STCD ORDER BY r.mm DESC LIMIT 1", []string{"businessdb.ST_PPTN_R", "businessdb.ST_STBPRP_B"}, "limit 1"},
		{"nested table", "SELECT STCD FROM ST_PPTN_R WHERE STCD IN (SELECT STCD FROM ST_STBPRP_B)", []string{"businessdb.ST_PPTN_R", "businessdb.ST_STBPRP_B"}, "limit 201"},
		{"cap large limit", "SELECT * FROM ST_PPTN_R LIMIT 500", []string{"businessdb.ST_PPTN_R"}, "limit 201"},
		{"write", "DELETE FROM ST_PPTN_R", nil, ""},
		{"unknown schema", "SELECT * FROM other.ST_PPTN_R", nil, ""},
		{"unknown nested table", "SELECT STCD FROM ST_PPTN_R WHERE STCD IN (SELECT STCD FROM missing)", nil, ""},
		{"two statements", "SELECT * FROM ST_PPTN_R; SELECT * FROM ST_STBPRP_B", nil, ""},
		{"locking read", "SELECT * FROM ST_PPTN_R FOR UPDATE", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query, tables, err := store.validate(tt.query)
			if tt.tables == nil {
				if err == nil {
					t.Fatalf("accepted a disallowed query: %s", query)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(tables, tt.tables) || !strings.HasSuffix(query, tt.limit) {
				t.Fatalf("got query=%q tables=%v err=%v", query, tables, err)
			}
		})
	}
}

func TestSQLToolResultEncoding(t *testing.T) {
	empty := QueryResult{Status: "empty", Columns: []string{"DYP"}, Rows: [][]any{}}
	data, err := resultMessage(&empty)
	if err != nil || !strings.Contains(data, `"rows":[]`) {
		t.Fatalf("empty result: %s, %v", data, err)
	}
	rows := [][]any{{"SC000042", "3.375"}, {strings.Repeat("雨", maxToolChars)}}
	full := QueryResult{Status: "ok", Columns: []string{"STCD", "DYP"}, Rows: rows}
	data, err = resultMessage(&full)
	var result QueryResult
	if err != nil || json.Unmarshal([]byte(data), &result) != nil || !result.Truncated ||
		result.RowCount != 1 || utf8.RuneCountInString(data) > maxToolChars || result.Rows[0][1] != "3.375" {
		t.Fatalf("truncated result: %+v, %v", result, err)
	}
}

func TestHistoryKeepsSuccessfulTurnsWithinSession(t *testing.T) {
	store := NewHistory()
	run := func(id, question string, completed bool, want []Exchange) {
		t.Helper()
		_, err := store.Run(context.Background(), id, func(turns []Exchange) (Answer, error) {
			if !reflect.DeepEqual(turns, want) {
				t.Fatalf("session %q: got %v, want %v", id, turns, want)
			}
			return Answer{Text: "answer", Completed: completed}, nil
		}, question)
		if err != nil {
			t.Fatal(err)
		}
	}
	run("a", "first", true, nil)
	run("b", "isolated", true, nil)
	run("", "stateless", true, nil)
	run("", "stateless", true, nil)
	prior := []Exchange{{Question: "first", Answer: "answer"}}
	run("a", "failed", false, prior)
	run("a", "follow-up", true, prior)
}
