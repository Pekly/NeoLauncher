//go:build !windows

package accounts

import "testing"

func TestNoPlaintextFallback(t *testing.T) {
	if s, err := NewService(t.TempDir(), DefaultClientID); err == nil {
		s.Close()
		t.Fatal("non-Windows protected store must fail closed")
	}
}
