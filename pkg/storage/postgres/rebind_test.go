package postgres

import "testing"

func TestRebindQuery(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{name: "no placeholders", query: `SELECT 1`, want: `SELECT 1`},
		{name: "one", query: `SELECT * FROM t WHERE id = ?`, want: `SELECT * FROM t WHERE id = $1`},
		{name: "many", query: `UPDATE t SET a = ?, b = ? WHERE id = ?`, want: `UPDATE t SET a = $1, b = $2 WHERE id = $3`},
		{name: "question mark in literal", query: `SELECT '?' AS q, ? AS v`, want: `SELECT '?' AS q, $1 AS v`},
		{name: "question mark in quoted identifier", query: `SELECT "a?b" FROM t WHERE id = ?`, want: `SELECT "a?b" FROM t WHERE id = $1`},
		{name: "line comment", query: "SELECT ? -- why?\n, ?", want: "SELECT $1 -- why?\n, $2"},
		{name: "block comment", query: `SELECT /* ? */ ?`, want: `SELECT /* ? */ $1`},
		{name: "nested block comment", query: `SELECT /* a /* ? */ b */ ?`, want: `SELECT /* a /* ? */ b */ $1`},
		{name: "escaped quote", query: `SELECT 'it''s ?' , ?`, want: `SELECT 'it''s ?' , $1`},
		{name: "escape clause", query: `DELETE FROM t WHERE id LIKE ? ESCAPE '\'`, want: `DELETE FROM t WHERE id LIKE $1 ESCAPE '\'`},
		{name: "dollar quoted", query: `SELECT $$?$$, $tag$?$tag$ , ?`, want: `SELECT $$?$$, $tag$?$tag$ , $1`},
		{name: "json existence operator", query: `SELECT data ?| array['a'], data ?& array['b'], ?`, want: `SELECT data ?| array['a'], data ?& array['b'], $1`},
		{name: "json literal path", query: `SELECT json_extract(spec, '$.a?b'), ?`, want: `SELECT json_extract(spec, '$.a?b'), $1`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := rebindQuery(tt.query)
			if err != nil {
				t.Fatalf("rebindQuery() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("rebindQuery() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRebindQueryRejectsTooManyParameters(t *testing.T) {
	query := make([]byte, 0, maxBindParameters*2+16)
	query = append(query, "SELECT "...)
	for i := 0; i < maxBindParameters+1; i++ {
		if i > 0 {
			query = append(query, ',')
		}
		query = append(query, '?')
	}
	if _, err := rebindQuery(string(query)); err == nil {
		t.Fatal("rebindQuery() error = nil, want parameter limit error")
	}
}

func TestRebindQueryRejectsMixedParameters(t *testing.T) {
	if _, err := rebindQuery(`SELECT $1, ?`); err == nil {
		t.Fatal("rebindQuery() error = nil, want mixed placeholder error")
	}
}
