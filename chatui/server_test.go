package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/sdk/go/chat"
)

// newTestServer builds a server with two accounts in front of a daemon stub.
func newTestServer(t *testing.T, daemon *daemonStub) *uiServer {
	t.Helper()
	backing, err := openStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	for _, name := range []string{"alice", "bob"} {
		if err := backing.addUser(name, name+"-password"); err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
	}
	client, err := chat.New(chat.Config{BaseURL: daemon.URL})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return &uiServer{
		client:   client,
		daemon:   daemon.URL,
		store:    backing,
		auth:     newAuthenticator(backing),
		sessions: map[string]*session{},
	}
}

// quietDaemon is a stub with no runs and no scripted turn, for the routes that
// never reach the agent.
func quietDaemon(t *testing.T) *daemonStub { return fakeDaemon(t, nil) }

// signIn returns the cookie a completed sign-in hands the browser.
func signIn(t *testing.T, server *uiServer, name string) *http.Cookie {
	t.Helper()
	body := strings.NewReader(`{"user":"` + name + `","password":"` + name + `-password"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/login", body)
	recorder := httptest.NewRecorder()
	server.routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("sign in as %s: %d %s", name, recorder.Code, recorder.Body)
	}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == sessionCookie {
			return cookie
		}
	}
	t.Fatal("sign in returned no session cookie")
	return nil
}

func call(t *testing.T, server *uiServer, cookie *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	request := httptest.NewRequest(method, path, reader)
	if cookie != nil {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	server.routes().ServeHTTP(recorder, request)
	return recorder
}

func createConversation(t *testing.T, server *uiServer, cookie *http.Cookie) string {
	t.Helper()
	recorder := call(t, server, cookie, http.MethodPost, "/api/conversations",
		`{"projectId":"p","agentName":"a"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("create: %d %s", recorder.Code, recorder.Body)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	return created.ID
}

func TestEveryAPIRouteNeedsASession(t *testing.T) {
	server := newTestServer(t, quietDaemon(t))
	guarded := []struct{ method, path string }{
		{http.MethodGet, "/api/agents"},
		{http.MethodGet, "/api/conversations"},
		{http.MethodPost, "/api/conversations"},
		{http.MethodGet, "/api/conversations/x"},
		{http.MethodPatch, "/api/conversations/x"},
		{http.MethodDelete, "/api/conversations/x"},
		{http.MethodGet, "/api/conversations/x/socket"},
		{http.MethodPost, "/api/conversations/recover"},
	}
	for _, route := range guarded {
		recorder := call(t, server, nil, route.method, route.path, "{}")
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s %s answered %d without a session, want 401", route.method, route.path, recorder.Code)
		}
	}
}

// A conversation belongs to one person. Someone else asking for it is told it
// does not exist rather than that they may not have it, because the existence
// of another user's conversation is itself none of their business.
func TestAConversationIsInvisibleToAnyoneButItsOwner(t *testing.T) {
	server := newTestServer(t, quietDaemon(t))
	alice, bob := signIn(t, server, "alice"), signIn(t, server, "bob")
	id := createConversation(t, server, alice)

	if recorder := call(t, server, alice, http.MethodGet, "/api/conversations/"+id, ""); recorder.Code != http.StatusOK {
		t.Fatalf("owner cannot read their own conversation: %d %s", recorder.Code, recorder.Body)
	}
	for _, attempt := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/conversations/" + id, ""},
		{http.MethodPatch, "/api/conversations/" + id, `{"title":"mine now"}`},
		{http.MethodDelete, "/api/conversations/" + id, ""},
		{http.MethodGet, "/api/conversations/" + id + "/socket", ""},
	} {
		recorder := call(t, server, bob, attempt.method, attempt.path, attempt.body)
		if recorder.Code != http.StatusNotFound {
			t.Errorf("%s by a stranger answered %d, want 404", attempt.method, recorder.Code)
		}
	}

	listed := call(t, server, bob, http.MethodGet, "/api/conversations", "")
	if strings.Contains(listed.Body.String(), id) {
		t.Errorf("another user's conversation appeared in the list: %s", listed.Body)
	}
}

func TestSignInRejectsTheWrongPasswordAndThenThrottles(t *testing.T) {
	server := newTestServer(t, quietDaemon(t))
	for attempt := range maxSignInFailures {
		recorder := call(t, server, nil, http.MethodPost, "/api/login", `{"user":"alice","password":"nope"}`)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d answered %d, want 401", attempt, recorder.Code)
		}
	}
	// The right password is refused too once the address has been guessing:
	// throttling is about the source, not the credential.
	recorder := call(t, server, nil, http.MethodPost, "/api/login", `{"user":"alice","password":"alice-password"}`)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("answered %d after %d failures, want 429", recorder.Code, maxSignInFailures)
	}
}

func TestSigningOutInvalidatesTheCookie(t *testing.T) {
	server := newTestServer(t, quietDaemon(t))
	alice := signIn(t, server, "alice")
	if recorder := call(t, server, alice, http.MethodPost, "/api/logout", ""); recorder.Code != http.StatusOK {
		t.Fatalf("sign out: %d %s", recorder.Code, recorder.Body)
	}
	if recorder := call(t, server, alice, http.MethodGet, "/api/conversations", ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("the old cookie still worked: %d", recorder.Code)
	}
}

// A conversation nobody has written to yet has never run, so deleting it must
// not need the daemon: there is nothing on the daemon to delete.
func TestDeletingAnUnusedConversationDoesNotNeedTheDaemon(t *testing.T) {
	server := newTestServer(t, quietDaemon(t))
	alice := signIn(t, server, "alice")
	id := createConversation(t, server, alice)

	recorder := call(t, server, alice, http.MethodDelete, "/api/conversations/"+id, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", recorder.Code, recorder.Body)
	}
	if strings.Contains(recorder.Body.String(), "warning") {
		t.Errorf("delete reached for the daemon: %s", recorder.Body)
	}
	if recorder := call(t, server, alice, http.MethodGet, "/api/conversations/"+id, ""); recorder.Code != http.StatusNotFound {
		t.Errorf("the conversation survived deletion: %d", recorder.Code)
	}
}

func TestTheChatListIsOrderedByLastUse(t *testing.T) {
	server := newTestServer(t, quietDaemon(t))
	alice := signIn(t, server, "alice")
	first := createConversation(t, server, alice)
	second := createConversation(t, server, alice)

	if err := server.store.touch("alice", first, "回到第一个对话"); err != nil {
		t.Fatalf("touch: %v", err)
	}
	records := server.store.conversations("alice")
	if len(records) != 2 || records[0].ID != first {
		t.Fatalf("want the just-used conversation first, got %+v", records)
	}
	if records[0].Title != "回到第一个对话" {
		t.Errorf("first message did not name the conversation: %q", records[0].Title)
	}
	if !records[0].Started {
		t.Error("a conversation that received a message is not marked started")
	}
	if records[1].ID != second || records[1].Started {
		t.Errorf("the untouched conversation changed: %+v", records[1])
	}
}

func TestTheStateFileSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	backing, err := openStore(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := backing.addUser("alice", "alice-password"); err != nil {
		t.Fatalf("add user: %v", err)
	}
	if _, err := backing.remember(conversationRecord{ID: "conv_1", Owner: "alice", Title: "旧对话"}); err != nil {
		t.Fatalf("remember: %v", err)
	}

	reopened, err := openStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := reopened.authenticate("alice", "alice-password"); err != nil {
		t.Errorf("the password did not survive a restart: %v", err)
	}
	if records := reopened.conversations("alice"); len(records) != 1 || records[0].Title != "旧对话" {
		t.Errorf("the chat list did not survive a restart: %+v", records)
	}
}

func TestAPasswordIsNeverStored(t *testing.T) {
	server := newTestServer(t, quietDaemon(t))
	raw, err := json.Marshal(server.store.data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "alice-password") {
		t.Fatal("the state file contains a password in the clear")
	}
	verifier := server.store.data.Users["alice"]
	if verifier.matches("alice-passwore") {
		t.Error("a wrong password verified")
	}
	if !verifier.matches("alice-password") {
		t.Error("the right password did not verify")
	}
}

func TestTruncateDoesNotSplitARune(t *testing.T) {
	// "你好世界" is four three-byte runes; a cut at 7 bytes lands mid-rune.
	if got := truncate("你好世界", 7); got != "你好…" {
		t.Fatalf("truncate cut a rune in half: %q", got)
	}
	if got := truncate("short", 10); got != "short" {
		t.Fatalf("truncate changed text that fits: %q", got)
	}
}

func TestTitlesComeFromTheFirstLineOfTheFirstMessage(t *testing.T) {
	if got := titleOf("  帮我看下这个 bug\n还有第二行  "); got != "帮我看下这个 bug" {
		t.Fatalf("title: %q", got)
	}
	if got := titleOf("   \n  "); got != "新对话" {
		t.Fatalf("an empty message should still name the conversation, got %q", got)
	}
}

// The UI groups a turn's work by the step an event names. An event that
// carries no step must not be given one, or the page would file it under a
// step it never belonged to.
func TestRenderedEventsCarryTheirStepAndOmitWhatWasNotReported(t *testing.T) {
	step := 2
	exit := int32(1)
	rendered := renderEvent(&chat.ToolCallEvent{
		Step: &step, ID: "call_1", Name: "bash", ToolKind: chat.ToolExecute,
		Status: chat.ToolCompleted, Command: "ls", ExitCode: &exit,
	})
	if rendered["step"] != 2 || rendered["exitCode"] != exit {
		t.Fatalf("tool call lost its step or exit code: %v", rendered)
	}
	if _, present := rendered["parentToolUseId"]; present {
		t.Errorf("an unreported field was sent anyway: %v", rendered)
	}

	unnumbered := renderEvent(&chat.TextDeltaEvent{Text: "hi"})
	if _, present := unnumbered["step"]; present {
		t.Errorf("an event with no step was given one: %v", unnumbered)
	}

	usage := renderEvent(&chat.UsageEvent{Scope: chat.ScopeTurn, InputTokens: 10, OutputTokens: 3})
	if usage["scope"] != string(chat.ScopeTurn) {
		t.Errorf("usage lost the scope that says whether it may be summed: %v", usage)
	}
	if _, present := usage["cachedTokens"]; present {
		t.Errorf("an unreported token count was sent as a zero: %v", usage)
	}
}

func TestASessionExpires(t *testing.T) {
	server := newTestServer(t, quietDaemon(t))
	alice := signIn(t, server, "alice")
	server.auth.mu.Lock()
	for key, found := range server.auth.sessions {
		found.expires = time.Now().Add(-time.Second)
		server.auth.sessions[key] = found
	}
	server.auth.mu.Unlock()

	if recorder := call(t, server, alice, http.MethodGet, "/api/conversations", ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("an expired session still worked: %d", recorder.Code)
	}
}

// Losing the index does not lose the conversations: ownership lives in the
// chat.user label, so a conversation this server has no record of still
// resolves for its owner and still hides from everyone else. Rebuilding the
// list is an explicit request, because it costs a run detail read per run.
func TestAConversationResolvesWithoutALocalRecord(t *testing.T) {
	daemon := quietDaemon(t)
	daemon.listRuns(
		map[string]any{
			"runId": "run_a", "projectId": "p", "agentName": "a",
			"status": "RUN_STATUS_RUNNING", "createdAt": time.Now().UTC().Format(time.RFC3339Nano),
			"labels": map[string]string{
				"chat.conversation": "conv_alice", "chat.user": "alice", "chat.app": appName,
			},
		},
		map[string]any{
			"runId": "run_b", "projectId": "p", "agentName": "a",
			"status": "RUN_STATUS_SUCCEEDED", "createdAt": time.Now().UTC().Format(time.RFC3339Nano),
			"labels": map[string]string{
				"chat.conversation": "conv_bob", "chat.user": "bob", "chat.app": appName,
			},
		},
	)
	// A fresh server: nothing in its store but the accounts.
	server := newTestServer(t, daemon)
	alice, bob := signIn(t, server, "alice"), signIn(t, server, "bob")

	// The sidebar is the local index, which knows nothing yet.
	listed := call(t, server, alice, http.MethodGet, "/api/conversations", "")
	var page struct {
		Conversations []map[string]any `json:"conversations"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Conversations) != 0 {
		t.Fatalf("the sidebar invented rows it has no record of: %v", page.Conversations)
	}

	// Ownership comes from the label, with no local record to consult.
	if recorder := call(t, server, bob, http.MethodGet, "/api/conversations/conv_alice", ""); recorder.Code != http.StatusNotFound {
		t.Errorf("a stranger reached a conversation the store never recorded: %d", recorder.Code)
	}
	if recorder := call(t, server, alice, http.MethodGet, "/api/conversations/conv_alice", ""); recorder.Code != http.StatusOK {
		t.Errorf("the owner could not open their own conversation: %d %s", recorder.Code, recorder.Body)
	}

	// Asked explicitly, the index is rebuilt from the daemon.
	rebuilt := call(t, server, alice, http.MethodPost, "/api/conversations/recover", "")
	if rebuilt.Code != http.StatusOK {
		t.Fatalf("recover: %d %s", rebuilt.Code, rebuilt.Body)
	}
	listed = call(t, server, alice, http.MethodGet, "/api/conversations", "")
	if err := json.Unmarshal(listed.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Conversations) != 1 || page.Conversations[0]["id"] != "conv_alice" {
		t.Fatalf("recover did not rebuild the list: %v", page.Conversations)
	}
	if bobs := call(t, server, bob, http.MethodPost, "/api/conversations/recover", ""); !strings.Contains(bobs.Body.String(), `"recovered":1`) {
		t.Errorf("bob recovered %s, want only his own conversation", bobs.Body)
	}
}

// A conversation nobody has written to yet has no run behind it, so the daemon
// has never heard of it. It still belongs in its owner's list.
func TestAConversationThatHasNotRunYetIsStillListed(t *testing.T) {
	server := newTestServer(t, quietDaemon(t))
	alice := signIn(t, server, "alice")
	id := createConversation(t, server, alice)

	listed := call(t, server, alice, http.MethodGet, "/api/conversations", "")
	var page struct {
		Conversations []map[string]any `json:"conversations"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Conversations) != 1 || page.Conversations[0]["id"] != id {
		t.Fatalf("a conversation with no run yet vanished from the list: %v", page.Conversations)
	}
	if page.Conversations[0]["started"] != false {
		t.Errorf("it should not claim to have run: %v", page.Conversations[0])
	}
}
