package nl2sql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

type Retrieval struct {
	BaseURL string
	HTTP    *http.Client
}

func (r Retrieval) Documents(ctx context.Context, path string, input any) (string, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(r.BaseURL, "/")+path, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("local retrieval service unavailable")
	}
	var payload struct {
		Documents []string `json:"documents"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return "", err
	}
	return strings.Join(payload.Documents, "\n\n"), nil
}

type Graph struct {
	Driver   neo4j.DriverWithContext
	Database string
}

func (g Graph) JoinPath(ctx context.Context, start, end string) ([]string, error) {
	if g.Driver == nil {
		return nil, errors.New("Neo4j is not configured")
	}
	result, err := neo4j.ExecuteQuery(ctx, g.Driver, `
MATCH p = shortestPath((start:Column {name: $start})-[:FOREIGN_KEY_TO|HAS_COLUMN*1..5]-(target:Column {name: $end}))
RETURN [n IN nodes(p) | n.name] AS names
LIMIT 1`, map[string]any{"start": start, "end": end}, neo4j.EagerResultTransformer,
		neo4j.ExecuteQueryWithDatabase(g.Database), neo4j.ExecuteQueryWithReadersRouting())
	if err != nil {
		return nil, err
	}
	if len(result.Records) == 0 {
		return nil, nil
	}
	names, _, err := neo4j.GetRecordValue[[]any](result.Records[0], "names")
	if err != nil {
		return nil, err
	}
	path := make([]string, 0, len(names))
	for _, name := range names {
		if s, ok := name.(string); ok {
			path = append(path, s)
		}
	}
	return path, nil
}
