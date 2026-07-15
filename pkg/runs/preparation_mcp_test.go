package runs

import "testing"

func TestDecodeRevisionSpecSupportsMCPServers(t *testing.T) {
	decoded, err := DecodeRevisionSpec(`{
		"name":"mcp-project",
		"mcp_servers":[{"name":"docs","type":"remote","transport":"http","url":"https://docs.example.com/mcp"}],
		"agents":[{"name":"reviewer","mcp_servers":[{"name":"docs","type":"remote","transport":"http","url":"https://docs.example.com/mcp"}]}]
	}`)
	if err != nil {
		t.Fatalf("DecodeRevisionSpec returned error: %v", err)
	}
	if len(decoded.GetMcpServers()) != 1 || decoded.GetMcpServers()[0].GetName() != "docs" {
		t.Fatalf("project mcp servers = %#v", decoded.GetMcpServers())
	}
	if len(decoded.GetAgents()) != 1 || len(decoded.GetAgents()[0].GetMcpServers()) != 1 {
		t.Fatalf("agent mcp servers = %#v", decoded.GetAgents())
	}
}
