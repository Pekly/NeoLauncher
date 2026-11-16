//go:build !windows

package accounts

// There is deliberately no plaintext or machine-independent fallback.
func newProtectedStore(root string) (secretStore, error) { return nil, errStorage }
