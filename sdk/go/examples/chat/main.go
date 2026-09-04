// Command chat holds a conversation with an agent-compose Agent from the
// terminal, resuming an earlier one when given its ID.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"

	"github.com/chaitin/agent-compose/sdk/go/chat"
)

func main() {
	baseURL := flag.String("base-url", "http://127.0.0.1:7410", "agent-compose HTTP address")
	project := flag.String("project", "", "project ID (required)")
	agentName := flag.String("agent", "", "agent name (required)")
	resume := flag.String("conversation", "", "conversation ID to resume; a new one is started when empty")
	flag.Parse()
	if *project == "" || *agentName == "" {
		flag.Usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	client, err := chat.New(chat.Config{
		BaseURL: *baseURL,
		Token:   chat.StaticToken(os.Getenv("AGENT_COMPOSE_TOKEN")),
	})
	if err != nil {
		log.Fatal(err)
	}
	agent := client.Agent(*project, *agentName)

	conversation := agent.Start()
	if *resume != "" {
		conversation, err = agent.Open(ctx, *resume)
		if err != nil {
			log.Fatal(err)
		}
		if err := replayHistory(ctx, conversation); err != nil {
			log.Fatal(err)
		}
		if conversation.Continuity() == chat.Restarted {
			fmt.Println("note: this conversation's environment was rebuilt, so the agent no longer has its earlier context")
		}
	}
	// Close leaves the conversation resumable; only Delete ends it.
	defer func() { _ = conversation.Close() }()
	fmt.Printf("conversation %s\n", conversation.ID())

	input := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("\n> ")
		if !input.Scan() {
			return
		}
		text := strings.TrimSpace(input.Text())
		if text == "" {
			continue
		}
		if err := turn(ctx, conversation, text); err != nil {
			log.Fatal(err)
		}
	}
}

// turn sends one message and renders the agent's work as it arrives.
func turn(ctx context.Context, conversation *chat.Conversation, text string) error {
	reply, err := conversation.Send(ctx, text)
	if err != nil {
		return err
	}
	for event, err := range reply.Events(ctx) {
		if err != nil {
			return err
		}
		switch typed := event.(type) {
		case *chat.TextDeltaEvent:
			fmt.Print(typed.Text)
		case *chat.ToolCallEvent:
			fmt.Printf("\n  [%s %s]", typed.ToolKind, typed.Name)
		case *chat.ToolResultEvent:
			if !typed.OK {
				fmt.Printf(" failed: %s", typed.Error)
			}
		case *chat.UsageEvent:
			fmt.Printf("\n  [%s: %d in, %d out]", typed.Scope, typed.InputTokens, typed.OutputTokens)
		case *chat.ErrorEvent:
			fmt.Printf("\n  [%s: %s]", typed.Severity, typed.Message)
		}
	}
	fmt.Println()
	return nil
}

func replayHistory(ctx context.Context, conversation *chat.Conversation) error {
	messages, err := conversation.History(ctx)
	if err != nil {
		return err
	}
	for _, message := range messages {
		fmt.Printf("%s: %s\n", message.Role, message.Text)
	}
	return nil
}
