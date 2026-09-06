module github.com/chaitin/agent-compose/chatui

go 1.25.0

require (
	github.com/chaitin/agent-compose/sdk/go v0.0.0
	github.com/gorilla/websocket v1.5.3
	golang.org/x/term v0.45.0
)

require (
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)

// The SDK is developed alongside this app and is not tagged yet.
replace github.com/chaitin/agent-compose/sdk/go => ../sdk/go
