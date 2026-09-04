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
	"cmp"
	"embed"
	"flag"
	"log"
	"net/http"
	"os"
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
	flag.Parse()

	client, err := chat.New(chat.Config{
		BaseURL:   *daemon,
		Token:     chat.StaticToken(cmp.Or(envToken(), "")),
		UserAgent: "agent-compose-chat-uiserver",
	})
	if err != nil {
		log.Fatal(err)
	}
	server := &uiServer{
		client:   client,
		daemon:   *daemon,
		sessions: map[string]*session{},
		upgrader: websocket.Upgrader{CheckOrigin: sameOriginOnly(strings.Split(*origins, ","))},
	}
	go server.release(time.Minute, 15*time.Minute)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", server.index)
	mux.HandleFunc("GET /api/agents", server.agents)
	mux.HandleFunc("POST /api/conversations", server.open)
	mux.HandleFunc("GET /api/conversations/{id}/history", server.history)
	mux.HandleFunc("GET /api/conversations/{id}/socket", server.socket)
	mux.HandleFunc("DELETE /api/conversations/{id}", server.remove)

	log.Printf("chat UI on http://%s (daemon %s)", *listen, *daemon)
	log.Fatal(http.ListenAndServe(*listen, mux))
}

func envToken() string { return strings.TrimSpace(os.Getenv("AGENT_COMPOSE_TOKEN")) }
