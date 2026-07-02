package auth

import "testing"

func TestIntegrationAuthWorkflows(t *testing.T) {
	testAuthWorkflows(t)
}

func TestE2EAuthWorkflows(t *testing.T) {
	testAuthWorkflows(t)
}

func testAuthWorkflows(t *testing.T) {
	t.Helper()
	t.Run("disabled auth allows requests", testAuthManagerDisabledAllowsRequests)
	t.Run("defaults username and ttl", testAuthManagerDefaultsUsernameAndTTL)
	t.Run("validates signed cookie", testAuthManagerValidatesSignedCookie)
	t.Run("rejects expired cookie", testAuthManagerRejectsExpiredCookie)
	t.Run("rejects tampered cookie", testAuthManagerRejectsTamperedCookie)
	t.Run("routes and middleware", testAuthManagerRoutesAndMiddleware)
	t.Run("oauth flow sets auth cookie", testAuthManagerOAuthFlowSetsAuthCookie)
	t.Run("oauth flow preserves subpath next", testAuthManagerOAuthFlowPreservesSubpathNext)
	t.Run("oauth token exchange failure", testAuthManagerOAuthTokenExchangeFailure)
}
