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

	"github.com/gorilla/websocket"

	"github.com/chaitin/agent-compose/sdk/go/chat"
)

type uiServer struct {
	client   *chat.Client
	daemon   string
	upgrader websocket.Upgrader

	mu       sync.Mutex
	sessions map[string]*session
}

// release closes conversations nobody is watching. Closing is not ending: the
// conversation survives on the daemon and a later resume reopens it with its
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

func (s *uiServer) open(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProjectID string `json:"projectId"`
		AgentName string `json:"agentName"`
		Resume    string `json:"resume"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	agent := s.client.Agent(body.ProjectID, body.AgentName)

	conversation := agent.Start()
	if body.Resume != "" {
		resumed, err := agent.Open(r.Context(), body.Resume)
		if err != nil {
			writeError(w, err)
			return
		}
		conversation = resumed
	}
	s.mu.Lock()
	s.sessions[conversation.ID()] = newSession(conversation)
	s.mu.Unlock()
	writeJSON(w, map[string]any{"id": conversation.ID(), "continuity": conversation.Continuity()})
}

func (s *uiServer) history(w http.ResponseWriter, r *http.Request) {
	found, ok := s.lookup(w, r)
	if !ok {
		return
	}
	messages, err := found.conversation.History(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	rendered := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		rendered = append(rendered, map[string]any{"role": message.Role, "text": message.Text, "time": message.Time})
	}
	writeJSON(w, map[string]any{"messages": rendered})
}

func (s *uiServer) remove(w http.ResponseWriter, r *http.Request) {
	found, ok := s.lookup(w, r)
	if !ok {
		return
	}
	err := found.conversation.Delete(r.Context())
	s.mu.Lock()
	delete(s.sessions, found.conversation.ID())
	s.mu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, map[string]any{"deleted": true})
}

func (s *uiServer) lookup(w http.ResponseWriter, r *http.Request) (*session, bool) {
	s.mu.Lock()
	found, ok := s.sessions[r.PathValue("id")]
	s.mu.Unlock()
	if !ok {
		http.Error(w, "unknown conversation", http.StatusNotFound)
		return nil, false
	}
	return found, true
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
	if token := envToken(); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
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
	case errors.Is(err, chat.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, chat.ErrInvalidArgument):
		status = http.StatusBadRequest
	case errors.Is(err, chat.ErrPermission):
		status = http.StatusForbidden
	}
	http.Error(w, err.Error(), status)
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}
