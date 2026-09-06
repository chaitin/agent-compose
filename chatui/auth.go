package main

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// sessionCookie holds the browser's proof of a completed sign-in.
	sessionCookie = "chatui_session"
	// sessionTTL is how long a sign-in lasts. Sessions live in memory only, so
	// a restart signs everyone out; that is the right default for a tool
	// someone runs on their own machine.
	sessionTTL = 12 * time.Hour

	// pbkdf2Iterations follows the OWASP guidance for PBKDF2-HMAC-SHA256. The
	// count is stored per credential so it can be raised later without
	// invalidating passwords already set.
	pbkdf2Iterations = 600_000
	pbkdf2KeyLength  = 32

	// maxSignInFailures bounds guessing from one address within failureWindow.
	maxSignInFailures = 10
	failureWindow     = 5 * time.Minute
)

// decoyCredential is verified when the named user does not exist, so a sign-in
// attempt costs the same whether or not the name is real and the response
// cannot be used to enumerate accounts.
var decoyCredential = credential{
	Salt:       make([]byte, 16),
	Hash:       make([]byte, pbkdf2KeyLength),
	Iterations: pbkdf2Iterations,
}

// newCredential derives a password verifier.
func newCredential(password string) (credential, error) {
	if len(password) < 8 {
		return credential{}, errors.New("password must be at least 8 characters")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return credential{}, err
	}
	hash, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, pbkdf2KeyLength)
	if err != nil {
		return credential{}, err
	}
	return credential{
		Salt:       salt,
		Hash:       hash,
		Iterations: pbkdf2Iterations,
		CreatedAt:  time.Now().UTC(),
	}, nil
}

// matches reports whether password derives this verifier. The comparison is
// constant time so it cannot be walked byte by byte.
func (c credential) matches(password string) bool {
	iterations := max(c.Iterations, 1)
	hash, err := pbkdf2.Key(sha256.New, password, c.Salt, iterations, max(len(c.Hash), 1))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(hash, c.Hash) == 1
}

// authenticator turns passwords into sessions and sessions back into users.
type authenticator struct {
	store *store

	mu       sync.Mutex
	sessions map[string]grant
	failures map[string]attempts
}

// grant is one live sign-in.
type grant struct {
	user    string
	expires time.Time
}

type attempts struct {
	count int
	since time.Time
}

func newAuthenticator(backing *store) *authenticator {
	return &authenticator{
		store:    backing,
		sessions: map[string]grant{},
		failures: map[string]attempts{},
	}
}

// userKey carries the signed-in user through a request.
type userKey struct{}

// userOf returns the signed-in user, which guard has already established.
func userOf(r *http.Request) string {
	name, _ := r.Context().Value(userKey{}).(string)
	return name
}

// guard rejects a request that carries no live session.
//
// It covers the WebSocket route as well: a handshake carries cookies like any
// other request, so the same check applies before the connection is upgraded.
func (a *authenticator) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name, ok := a.resolve(r)
		if !ok {
			http.Error(w, "sign in required", http.StatusUnauthorized)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey{}, name)))
	}
}

// resolve reads the session cookie.
func (a *authenticator) resolve(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return "", false
	}
	key := tokenKey(cookie.Value)
	a.mu.Lock()
	defer a.mu.Unlock()
	found, ok := a.sessions[key]
	if !ok {
		return "", false
	}
	if time.Now().After(found.expires) {
		delete(a.sessions, key)
		return "", false
	}
	return found.user, true
}

// signIn checks a password and issues a session cookie.
func (a *authenticator) signIn(w http.ResponseWriter, r *http.Request) {
	var body struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	if a.throttled(r) {
		http.Error(w, "too many attempts, wait a few minutes", http.StatusTooManyRequests)
		return
	}
	name := strings.TrimSpace(body.User)
	if err := a.store.authenticate(name, body.Password); err != nil {
		a.recordFailure(r)
		http.Error(w, "wrong user name or password", http.StatusUnauthorized)
		return
	}
	token, err := a.issue(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		// Lax keeps the cookie off cross-site requests that could act on the
		// user's behalf while still surviving a normal navigation to the page.
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	a.clearFailures(r)
	writeJSON(w, map[string]any{"user": name})
}

// signOut drops the session and the cookie.
func (a *authenticator) signOut(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		a.mu.Lock()
		delete(a.sessions, tokenKey(cookie.Value))
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil, MaxAge: -1,
	})
	writeJSON(w, map[string]any{"signedOut": true})
}

// whoami tells the page whether it still has a session, so it can show the
// sign-in form without first failing a real request.
func (a *authenticator) whoami(w http.ResponseWriter, r *http.Request) {
	name, ok := a.resolve(r)
	if !ok {
		writeJSON(w, map[string]any{"user": nil})
		return
	}
	writeJSON(w, map[string]any{"user": name})
}

// issue mints a session token. Only its digest is retained, so nothing that
// reads this process's session table learns a usable cookie.
func (a *authenticator) issue(name string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessions[tokenKey(token)] = grant{user: name, expires: time.Now().Add(sessionTTL)}
	return token, nil
}

// sweep discards expired sessions and stale failure counters.
func (a *authenticator) sweep(every time.Duration) {
	for now := range time.Tick(every) {
		a.mu.Lock()
		for key, found := range a.sessions {
			if now.After(found.expires) {
				delete(a.sessions, key)
			}
		}
		for key, found := range a.failures {
			if now.Sub(found.since) > failureWindow {
				delete(a.failures, key)
			}
		}
		a.mu.Unlock()
	}
}

func (a *authenticator) throttled(r *http.Request) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	found, ok := a.failures[clientAddress(r)]
	return ok && found.count >= maxSignInFailures && time.Since(found.since) < failureWindow
}

func (a *authenticator) recordFailure(r *http.Request) {
	address := clientAddress(r)
	a.mu.Lock()
	defer a.mu.Unlock()
	found := a.failures[address]
	if time.Since(found.since) > failureWindow {
		found = attempts{since: time.Now()}
	}
	found.count++
	a.failures[address] = found
}

func (a *authenticator) clearFailures(r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.failures, clientAddress(r))
}

// clientAddress identifies the caller for throttling. Only the socket address
// is used: a forwarded-for header is written by whoever is calling.
func clientAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func tokenKey(token string) string {
	digest := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}
