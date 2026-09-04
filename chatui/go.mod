module github.com/chaitin/agent-compose/chatui

go 1.23.0

require github.com/chaitin/agent-compose/sdk/go v0.0.0

require (
	golang.org/x/net v0.38.0 // indirect
	golang.org/x/text v0.23.0 // indirect
)

// The SDK is developed alongside this app and is not tagged yet.
replace github.com/chaitin/agent-compose/sdk/go => ../sdk/go
