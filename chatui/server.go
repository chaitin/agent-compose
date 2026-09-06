package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"github.com/chaitin/agent-compose/sdk/go/chat"
)

// ownerLabel and appLabel are attached to every run this server starts, so a
// conversation is attributable on the daemon side without consulting this
// server's own state file.
const (
	ownerLabel = "chat.user"
	appLabel   = "chat.app"
	appName    = "chatui"
)

type uiServer struct {
	client   *chat.Client
	daemon   string
	token    string
	store    *store
	auth     *authenticator
	upgrader websocket.Upgrader

	mu       sync.Mutex
	sessions map[string]*session
}

// routes wires every endpoint. Everything but signing in and the page itself
// requires a session.
func (s *uiServer) routes() *http.ServeMux {
	mux := http.NewServeMux()
	guard := s.auth.guard

	mux.HandleFunc("GET /", s.index)
	mux.HandleFunc("POST /api/login", s.auth.signIn)
	mux.HandleFunc("POST /api/logout", s.auth.signOut)
	mux.HandleFunc("GET /api/me", s.auth.whoami)

	mux.HandleFunc("GET /api/agents", guard(s.agents))
	mux.HandleFunc("GET /api/conversations", guard(s.list))
	mux.HandleFunc("POST /api/conversations/recover", guard(s.reindex))
	mux.HandleFunc("POST /api/conversations", guard(s.create))
	mux.HandleFunc("GET /api/conversations/{id}", guard(s.describe))
	mux.HandleFunc("PATCH /api/conversations/{id}", guard(s.rename))
	mux.HandleFunc("DELETE /api/conversations/{id}", guard(s.remove))
	mux.HandleFunc("GET /api/conversations/{id}/socket", guard(s.socket))
	return mux
}

// release closes conversations nobody is watching. Closing is not ending: the
// conversation survives on the daemon and a later attach reopens it with its
// context intact, so an unattended browser tab costs this process nothing.
func (s *uiServer) release(every, after time.Duration) {
	for range time.Tick(every) {
		s.mu.Lock()
		for id, found := range s.sessions {
			if found.releasable(after) {
				_ = found.conversation.Close()
				delete(s.sessions, id)
			}
		}
		s.mu.Unlock()
	}
}

func (s *uiServer) index(w http.ResponseWriter, _ *http.Request) {
	page, err := assets.ReadFile("public/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(page)
}

// agents lists what the user can talk to.
//
// This is the one thing the chat SDK deliberately does not cover: picking a
// counterpart is not part of holding a conversation. Two plain Connect calls
// are all it takes.
func (s *uiServer) agents(w http.ResponseWriter, r *http.Request) {
	var projects struct {
		Projects []struct {
			ProjectID string `json:"projectId"`
			Name      string `json:"name"`
		} `json:"projects"`
	}
	if err := s.connect(r.Context(), "ProjectService", "ListProjects", map[string]any{"limit": 200}, &projects); err != nil {
		writeError(w, err)
		return
	}
	type entry struct {
		ProjectID string   `json:"projectId"`
		Name      string   `json:"name"`
		Agents    []string `json:"agents"`
	}
	result := make([]entry, 0, len(projects.Projects))
	for _, project := range projects.Projects {
		var detail struct {
			Project struct {
				Agents []struct {
					AgentName string `json:"agentName"`
				} `json:"agents"`
			} `json:"project"`
		}
		request := map[string]any{"project": map[string]any{"projectId": project.ProjectID}}
		if err := s.connect(r.Context(), "ProjectService", "GetProject", request, &detail); err != nil {
			continue
		}
		names := make([]string, 0, len(detail.Project.Agents))
		for _, agent := range detail.Project.Agents {
			names = append(names, agent.AgentName)
		}
		result = append(result, entry{ProjectID: project.ProjectID, Name: project.Name, Agents: names})
	}
	writeJSON(w, map[string]any{"projects": result})
}

// list draws the sidebar from this server's own index.
//
// The daemon could answer it — every run carries chat.user and
// chat.conversation — but only at one GetRun per run: a run summary carries no
// labels by design, so grouping a page of runs into conversations means
// reading each one's detail. The sidebar is redrawn after every turn, so it
// reads the local index instead and rebuilds it on demand (see reindex).
func (s *uiServer) list(w http.ResponseWriter, r *http.Request) {
	records := s.store.conversations(userOf(r))
	rows := make([]map[string]any, 0, len(records))
	for _, record := range records {
		rows = append(rows, s.describeRecord(record))
	}
	writeJSON(w, map[string]any{"conversations": rows})
}

// reindex rebuilds the index from the daemon, for when this server has lost it
// or never had it: a new machine, a deleted state file, a second instance.
//
// This is the expensive path the sidebar deliberately avoids, so it runs only
// when asked. Conversations already indexed keep their titles; recovered ones
// have none, because a title was never the daemon's to hold.
func (s *uiServer) reindex(w http.ResponseWriter, r *http.Request) {
	owner := userOf(r)
	known, err := s.client.Conversations(r.Context(), chat.Search{
		Labels: map[string]string{ownerLabel: owner, appLabel: appName},
	})
	if err != nil {
		writeError(w, err)
		return
	}
	added := 0
	for _, found := range known {
		if _, err := s.store.conversation(owner, found.ID); err == nil {
			continue
		}
		if _, err := s.store.remember(recordOf(owner, found)); err != nil {
			writeError(w, err)
			return
		}
		added++
	}
	writeJSON(w, map[string]any{"recovered": added, "found": len(known)})
}

// recordOf indexes a conversation only the daemon knows about.
func recordOf(owner string, found chat.ConversationInfo) conversationRecord {
	return conversationRecord{
		ID:        found.ID,
		Owner:     owner,
		ProjectID: found.ProjectID,
		AgentName: found.AgentName,
		Started:   true,
		CreatedAt: found.LastActive,
		UpdatedAt: found.LastActive,
	}
}

// create starts a new conversation. No daemon call is made: the environment is
// provisioned by the first message, so an abandoned "new chat" costs nothing.
func (s *uiServer) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProjectID string `json:"projectId"`
		AgentName string `json:"agentName"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.ProjectID) == "" || strings.TrimSpace(body.AgentName) == "" {
		http.Error(w, "projectId and agentName are required", http.StatusBadRequest)
		return
	}
	owner := userOf(r)
	// The labels go on at creation, not at attach: they are what makes the run
	// this conversation eventually starts attributable to its owner, and the
	// start frame is the only chance to set them.
	conversation := s.client.Agent(body.ProjectID, body.AgentName).Start(s.labels(owner))
	record := conversationRecord{
		ID:        conversation.ID(),
		Owner:     owner,
		ProjectID: body.ProjectID,
		AgentName: body.AgentName,
	}
	record, err := s.store.remember(record)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	s.sessions[record.ID] = newSession(conversation, record)
	s.mu.Unlock()
	writeJSON(w, s.describeRecord(record))
}

// describe returns one conversation with its transcript, which is what opening
// it from the sidebar needs. Reading history does not attach: that happens when
// the socket opens.
func (s *uiServer) describe(w http.ResponseWriter, r *http.Request) {
	record, ok := s.record(w, r)
	if !ok {
		return
	}
	payload := s.describeRecord(record)
	messages := []map[string]any{}
	if record.Started {
		history, err := s.handle(record).History(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		for _, message := range history {
			messages = append(messages, map[string]any{
				"role": message.Role, "text": message.Text, "time": message.Time,
			})
		}
	}
	payload["messages"] = messages
	writeJSON(w, payload)
}

func (s *uiServer) rename(w http.ResponseWriter, r *http.Request) {
	record, ok := s.record(w, r)
	if !ok {
		return
	}
	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	if err := s.store.rename(record.Owner, record.ID, body.Title); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	updated, err := s.store.conversation(record.Owner, record.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, s.describeRecord(updated))
}

// remove ends the conversation on the daemon and drops it from the sidebar.
//
// It works whether or not this process is currently attached: a conversation
// that was released while nobody watched still has to be deletable.
func (s *uiServer) remove(w http.ResponseWriter, r *http.Request) {
	record, ok := s.record(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	live, attached := s.sessions[record.ID]
	delete(s.sessions, record.ID)
	s.mu.Unlock()

	conversation := s.handle(record)
	if attached {
		conversation = live.conversation
	}
	var failure error
	if record.Started {
		failure = conversation.Delete(r.Context())
	} else {
		failure = conversation.Close()
	}
	if err := s.store.forget(record.Owner, record.ID); err != nil && !errors.Is(err, errNoSuchConversation) {
		writeError(w, err)
		return
	}
	// The sidebar entry is gone either way. A daemon that could not be reached
	// is reported, not hidden, but it does not resurrect the row.
	if failure != nil {
		writeJSON(w, map[string]any{"deleted": true, "warning": failure.Error()})
		return
	}
	writeJSON(w, map[string]any{"deleted": true})
}

// attach returns the live session for a conversation, opening one if this
// process is not currently holding it.
//
// A conversation that has never run is started rather than opened: opening one
// with no runs behind it would report it as restarted, which would be a lie
// about a conversation that has nothing to restart.
func (s *uiServer) attach(ctx context.Context, record conversationRecord) (*session, error) {
	s.mu.Lock()
	found, ok := s.sessions[record.ID]
	s.mu.Unlock()
	if ok {
		return found, nil
	}
	agent := s.client.Agent(record.ProjectID, record.AgentName)
	conversation := agent.Start(chat.WithID(record.ID), s.labels(record.Owner))
	if record.Started {
		resumed, err := agent.Open(ctx, record.ID, s.labels(record.Owner))
		if err != nil {
			return nil, err
		}
		conversation = resumed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Another request may have attached while this one was talking to the
	// daemon. The first one wins; a second live stream on one conversation
	// would have both of them competing for its turns.
	if found, ok := s.sessions[record.ID]; ok {
		_ = conversation.Close()
		return found, nil
	}
	opened := newSession(conversation, record)
	s.sessions[record.ID] = opened
	return opened, nil
}

// handle returns a conversation handle for the operations that need no live
// stream: reading history, and deleting.
func (s *uiServer) handle(record conversationRecord) *chat.Conversation {
	agent := s.client.Agent(record.ProjectID, record.AgentName)
	return agent.Start(chat.WithID(record.ID), s.labels(record.Owner))
}

// labels mark the run on the daemon with who it belongs to, so a conversation
// can be traced back to a person from the daemon's own tooling.
func (s *uiServer) labels(owner string) chat.Option {
	return chat.WithLabels(map[string]string{ownerLabel: owner, appLabel: appName})
}

// describeRecord renders one sidebar row.
func (s *uiServer) describeRecord(record conversationRecord) map[string]any {
	s.mu.Lock()
	live, attached := s.sessions[record.ID]
	s.mu.Unlock()
	row := map[string]any{
		"id":        record.ID,
		"projectId": record.ProjectID,
		"agentName": record.AgentName,
		"title":     record.Title,
		"started":   record.Started,
		"createdAt": record.CreatedAt,
		"updatedAt": record.UpdatedAt,
		"attached":  attached,
		"running":   attached && live.running(),
	}
	if attached {
		row["continuity"] = live.conversation.Continuity()
	}
	return row
}

// record resolves the path's conversation, and only for the user who owns it.
//
// The local index is consulted first because it is free, but a miss is not an
// answer: ownership lives in the chat.user label on the daemon, not here.
// Lookup settles it in one call — the run list's label filter answers "is
// there a conversation with this ID carrying these labels?" without reading a
// single label back — so a conversation still works after this server has lost
// or never had a record of it.
func (s *uiServer) record(w http.ResponseWriter, r *http.Request) (conversationRecord, bool) {
	owner, id := userOf(r), r.PathValue("id")
	if found, err := s.store.conversation(owner, id); err == nil {
		return found, true
	}
	found, ok, err := s.client.Lookup(r.Context(), id, map[string]string{ownerLabel: owner, appLabel: appName})
	if err != nil || !ok {
		// Someone else's conversation is reported absent rather than
		// forbidden: that it exists is none of the asker's business.
		http.Error(w, "unknown conversation", http.StatusNotFound)
		return conversationRecord{}, false
	}
	return recordOf(owner, found), true
}

// connect makes one plain Connect JSON call, for the services the chat SDK
// does not cover.
func (s *uiServer) connect(ctx context.Context, service, method string, in, out any) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/agentcompose.v2.%s/%s", strings.TrimRight(s.daemon, "/"), service, method)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Connect-Protocol-Version", "1")
	if s.token != "" {
		request.Header.Set("Authorization", "Bearer "+s.token)
	}
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
		return fmt.Errorf("%s failed with HTTP %d: %s", method, response.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(response.Body).Decode(out)
}

func writeJSON(w http.ResponseWriter, payload any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(encoded)
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	switch {
	case errors.Is(err, chat.ErrNotFound), errors.Is(err, errNoSuchConversation):
		status = http.StatusNotFound
	case errors.Is(err, chat.ErrInvalidArgument):
		status = http.StatusBadRequest
	case errors.Is(err, chat.ErrPermission):
		status = http.StatusForbidden
	}
	http.Error(w, err.Error(), status)
}

// truncate shortens text to limit bytes without splitting a rune, so a cut
// Chinese title stays readable instead of ending in a replacement character.
func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}
