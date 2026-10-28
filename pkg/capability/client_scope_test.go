package capability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientOmitsScopeHeadersWhenScopeEmpty(t *testing.T) {
	var project, username string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		project = r.Header.Get(ProjectScopeHeader)
		username = r.Header.Get(ExtUsernameHeader)
		_, _ = w.Write([]byte("# guide"))
	}))
	defer server.Close()

	client := NewClient(Config{Addr: server.URL, Token: "token", Scope: Scope{ProjectID: "  ", Username: "\t"}})
	if _, err := client.CatalogMarkdown(context.Background(), "dev"); err != nil {
		t.Fatal(err)
	}
	if project != "" || username != "" {
		t.Fatalf("scope headers = %q/%q, want none", project, username)
	}
}

func TestClientSendsScopeHeadersOnDirectoryReads(t *testing.T) {
	type requestHeaders struct{ project, username, accept string }
	var got []requestHeaders
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, requestHeaders{
			project:  r.Header.Get(ProjectScopeHeader),
			username: r.Header.Get(ExtUsernameHeader),
			accept:   r.Header.Get("Accept"),
		})
		_, _ = w.Write([]byte(`{"capset_id":"dev"}`))
	}))
	defer server.Close()

	client := NewClient(Config{Addr: server.URL, Token: "token", Scope: Scope{ProjectID: "project-1", Username: "alice"}})
	if _, err := client.Catalog(context.Background(), "dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CatalogMarkdown(context.Background(), "dev"); err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 {
		t.Fatalf("requests = %d, want 2", len(got))
	}
	for i, headers := range got {
		if headers.project != "project-1" || headers.username != "alice" {
			t.Fatalf("request %d scope headers = %q/%q", i, headers.project, headers.username)
		}
	}
	if got[0].accept != "application/json" || got[1].accept != "text/markdown" {
		t.Fatalf("accept headers = %q/%q", got[0].accept, got[1].accept)
	}
}

func TestClientSkipsUnsendableScopeValues(t *testing.T) {
	var project, username string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		project = r.Header.Get(ProjectScopeHeader)
		username = r.Header.Get(ExtUsernameHeader)
		_, _ = w.Write([]byte("# guide"))
	}))
	defer server.Close()

	client := NewClient(Config{Addr: server.URL, Scope: Scope{ProjectID: "project\n1", Username: "alice"}})
	if _, err := client.CatalogMarkdown(context.Background(), "dev"); err != nil {
		t.Fatal(err)
	}
	if project != "" {
		t.Fatalf("project header = %q, want none for an unsendable value", project)
	}
	if username != "alice" {
		t.Fatalf("username header = %q, want alice", username)
	}
}
