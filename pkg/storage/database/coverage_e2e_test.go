package database

import "testing"

// TestE2EPostgresDatabaseOwner runs the PostgreSQL owner lifecycle under the
// end-to-end shape. The daemon's single-instance guarantee and lock release
// span component startup and shutdown, so the coverage gate must see the
// composition path rather than only the integration shape. The behavior itself
// is pinned by TestPostgresDatabaseOwnerIntegration.
func TestE2EPostgresDatabaseOwner(t *testing.T) {
	TestPostgresDatabaseOwnerIntegration(t)
}
