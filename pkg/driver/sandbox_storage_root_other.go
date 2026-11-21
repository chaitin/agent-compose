//go:build !unix

package driver

// Microsandbox runs only on unix hosts; there is no ancestor rule to mirror.
func checkStorageRootAncestorsOnHost(string) error {
	return nil
}
