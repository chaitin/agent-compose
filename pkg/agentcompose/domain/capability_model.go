package domain

// CapabilityGatewaySettings is the page-configured OctoBus connection. It is
// persisted as a single row in the capability_gateway table and read
// dynamically at request time. The deployment-fixed proxy listen/target
// addresses are intentionally not stored here.
type CapabilityGatewaySettings struct {
	Addr  string
	Token string
}
