package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// errNoSuchUser and errNoSuchConversation separate "not yours" from "broken"
// at the call sites that turn them into HTTP status codes.
var (
	errNoSuchUser         = errors.New("unknown user or wrong password")
	errNoSuchConversation = errors.New("unknown conversation")
	errUserExists         = errors.New("user already exists")
)

// store is this server's own record of who may sign in and what conversations
// they own.
//
// The daemon owns the conversations themselves; this file holds only what a
// chat list needs and the daemon does not keep. Run labels cannot stand in for
// it: a run's labels are fixed when it starts, so a title could never be
// renamed, and ListRuns returns summaries without labels, so drawing a sidebar
// would cost one GetRun per row.
type store struct {
	path string

	mu   sync.Mutex
	data snapshot
}

type snapshot struct {
	Users         map[string]credential `json:"users"`
	Conversations []conversationRecord  `json:"conversations"`
}

// credential is a password verifier, never a password.
type credential struct {
	Salt       []byte    `json:"salt"`
	Hash       []byte    `json:"hash"`
	Iterations int       `json:"iterations"`
	CreatedAt  time.Time `json:"createdAt,omitzero"`
}

// conversationRecord is one row of a user's chat list. The conversation it
// names lives on the daemon and is reached through the SDK by ID.
type conversationRecord struct {
	ID        string `json:"id"`
	Owner     string `json:"owner"`
	ProjectID string `json:"projectId"`
	AgentName string `json:"agentName"`
	Title     string `json:"title"`
	// Started records that at least one message has been sent, which is what
	// separates a conversation to resume from one that has never run.
	Started   bool      `json:"started,omitzero"`
	CreatedAt time.Time `json:"createdAt,omitzero"`
	UpdatedAt time.Time `json:"updatedAt,omitzero"`
}

// openStore reads the state file, creating an empty one if it is absent.
func openStore(path string) (*store, error) {
	opened := &store{path: path, data: snapshot{Users: map[string]credential{}}}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return opened, nil
	case err != nil:
		return nil, err
	}
	if err := json.Unmarshal(raw, &opened.data); err != nil {
		return nil, fmt.Errorf("state file %s is not readable: %w", path, err)
	}
	if opened.data.Users == nil {
		opened.data.Users = map[string]credential{}
	}
	return opened, nil
}

// save writes the state file atomically, so a crash mid-write cannot leave a
// half-written file where the user list used to be.
//
// Callers hold s.mu.
func (s *store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	temporary := s.path + ".tmp"
	if err := os.WriteFile(temporary, append(encoded, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, s.path)
}

// users reports how many accounts exist, which is how startup decides whether
// to bootstrap one.
func (s *store) users() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.data.Users)
}

// addUser registers an account. It refuses to overwrite an existing one:
// changing a password is a different intent from creating an account, and
// conflating them turns a typo into a lockout.
func (s *store) addUser(name, password string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("user name is required")
	}
	verifier, err := newCredential(password)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data.Users[name]; exists {
		return errUserExists
	}
	s.data.Users[name] = verifier
	return s.save()
}

// setPassword replaces an existing account's password.
func (s *store) setPassword(name, password string) error {
	verifier, err := newCredential(password)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data.Users[name]; !exists {
		return errNoSuchUser
	}
	verifier.CreatedAt = s.data.Users[name].CreatedAt
	s.data.Users[name] = verifier
	return s.save()
}

// authenticate reports whether the password matches. An unknown user is
// checked against a dummy verifier anyway, so the reply takes the same time
// either way and does not reveal which names exist.
func (s *store) authenticate(name, password string) error {
	s.mu.Lock()
	verifier, known := s.data.Users[name]
	s.mu.Unlock()
	if !known {
		verifier = decoyCredential
	}
	if !verifier.matches(password) || !known {
		return errNoSuchUser
	}
	return nil
}

// conversations returns one user's chat list, most recently used first.
func (s *store) conversations(owner string) []conversationRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	owned := make([]conversationRecord, 0, len(s.data.Conversations))
	for _, record := range s.data.Conversations {
		if record.Owner == owner {
			owned = append(owned, record)
		}
	}
	slices.SortStableFunc(owned, func(a, b conversationRecord) int {
		return b.UpdatedAt.Compare(a.UpdatedAt)
	})
	return owned
}

// conversation returns one record, and only to the user who owns it. A
// conversation someone else owns is reported as absent rather than forbidden:
// the asker has no business learning that it exists.
func (s *store) conversation(owner, id string) (conversationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := s.indexOf(owner, id)
	if index < 0 {
		return conversationRecord{}, errNoSuchConversation
	}
	return s.data.Conversations[index], nil
}

// remember adds a conversation to its owner's list and returns it as stored,
// timestamps and all, so a caller does not render the copy it passed in.
func (s *store) remember(record conversationRecord) (conversationRecord, error) {
	now := time.Now().UTC()
	record.CreatedAt, record.UpdatedAt = now, now
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Conversations = append(s.data.Conversations, record)
	return record, s.save()
}

// touch marks a conversation as used and, the first time, names it.
//
// A conversation is named after the message that opened it, which is what the
// person who comes back to it will recognise. Later messages only reorder the
// list.
func (s *store) touch(owner, id, firstMessage string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := s.indexOf(owner, id)
	if index < 0 {
		return errNoSuchConversation
	}
	s.data.Conversations[index].UpdatedAt = time.Now().UTC()
	s.data.Conversations[index].Started = true
	if s.data.Conversations[index].Title == "" {
		s.data.Conversations[index].Title = titleOf(firstMessage)
	}
	return s.save()
}

// rename replaces a conversation's title.
func (s *store) rename(owner, id, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("title is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index := s.indexOf(owner, id)
	if index < 0 {
		return errNoSuchConversation
	}
	s.data.Conversations[index].Title = truncate(title, 120)
	return s.save()
}

// forget drops a conversation from its owner's list.
func (s *store) forget(owner, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := s.indexOf(owner, id)
	if index < 0 {
		return errNoSuchConversation
	}
	s.data.Conversations = slices.Delete(s.data.Conversations, index, index+1)
	return s.save()
}

// indexOf locates one user's conversation. Callers hold s.mu.
func (s *store) indexOf(owner, id string) int {
	return slices.IndexFunc(s.data.Conversations, func(record conversationRecord) bool {
		return record.ID == id && record.Owner == owner
	})
}

// titleOf names a conversation after its opening message.
func titleOf(message string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(message), "\n")
	if line = strings.TrimSpace(line); line == "" {
		return "新对话"
	}
	return truncate(line, 60)
}
