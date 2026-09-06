// Command chatui serves a browser chat UI for agent-compose agents.
//
// The browser cannot hold a conversation with the daemon directly: Connect
// needs HTTP/2 bidirectional streaming for a multi-turn session, and a browser
// fetch() with a streaming request body is half duplex, so nothing comes back
// while that body stays open. This app keeps the conversation on the Go side,
// where the chat SDK handles it, and gives the browser the one-way event
// stream it can actually consume.
package main

import (
	"crypto/rand"
	"embed"
	"encoding/base64"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chaitin/agent-compose/sdk/go/chat"
)

//go:embed public/index.html
var assets embed.FS

func main() {
	listen := flag.String("listen", "127.0.0.1:7500", "address to serve the UI on")
	daemon := flag.String("daemon", "http://127.0.0.1:7411", "agent-compose HTTP address")
	origins := flag.String("allow-origin", "", "comma-separated extra origins allowed to open a chat socket")
	state := flag.String("state", defaultStatePath(), "state file holding accounts and the chat list")
	addUser := flag.String("add-user", "", "create an account with this name, then exit")
	setPassword := flag.String("set-password", "", "change this account's password, then exit")
	flag.Parse()

	backing, err := openStore(*state)
	if err != nil {
		log.Fatal(err)
	}
	if *addUser != "" || *setPassword != "" {
		if err := manageAccount(backing, *addUser, *setPassword); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := bootstrap(backing); err != nil {
		log.Fatal(err)
	}

	token := envToken()
	client, err := chat.New(chat.Config{
		BaseURL:   *daemon,
		Token:     chat.StaticToken(token),
		UserAgent: "agent-compose-chat-uiserver",
	})
	if err != nil {
		log.Fatal(err)
	}
	auth := newAuthenticator(backing)
	server := &uiServer{
		client:   client,
		daemon:   *daemon,
		token:    token,
		store:    backing,
		auth:     auth,
		sessions: map[string]*session{},
		upgrader: websocket.Upgrader{CheckOrigin: sameOriginOnly(strings.Split(*origins, ","))},
	}
	go server.release(time.Minute, 15*time.Minute)
	go auth.sweep(time.Minute)

	log.Printf("chat UI on http://%s (daemon %s, state %s)", *listen, *daemon, *state)
	log.Fatal(http.ListenAndServe(*listen, server.routes()))
}

// manageAccount runs the two account commands, which exist so a password never
// has to be typed on a command line where the shell history keeps it.
func manageAccount(backing *store, add, reset string) error {
	if add != "" {
		password, err := readPassword("password for " + add + ": ")
		if err != nil {
			return err
		}
		if err := backing.addUser(add, password); err != nil {
			return err
		}
		log.Printf("created %s", add)
		return nil
	}
	password, err := readPassword("new password for " + reset + ": ")
	if err != nil {
		return err
	}
	if err := backing.setPassword(reset, password); err != nil {
		return err
	}
	log.Printf("password changed for %s", reset)
	return nil
}

// bootstrap creates a first account when there is none, printing a generated
// password once. A UI with no accounts would otherwise be unreachable, and a
// fixed default password would be worse than none.
func bootstrap(backing *store) error {
	if backing.users() > 0 {
		return nil
	}
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	password := base64.RawURLEncoding.EncodeToString(raw)
	if err := backing.addUser("admin", password); err != nil {
		return err
	}
	log.Printf("no accounts yet — created admin with password %s", password)
	log.Printf("this password is not shown again; change it with -set-password admin")
	return nil
}

// readPassword takes a password off stdin without echoing it when stdin is a
// terminal, and reads a piped line otherwise so the command stays scriptable.
func readPassword(prompt string) (string, error) {
	_, _ = os.Stderr.WriteString(prompt)
	defer func() { _, _ = os.Stderr.WriteString("\n") }()
	password, err := readSecret(os.Stdin)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(password) == "" {
		return "", errors.New("password is required")
	}
	return password, nil
}

// defaultStatePath keeps state beside the rest of a user's agent-compose data.
func defaultStatePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "chatui-state.json"
	}
	return filepath.Join(home, ".agent-compose", "chatui", "state.json")
}

func envToken() string { return strings.TrimSpace(os.Getenv("AGENT_COMPOSE_TOKEN")) }
