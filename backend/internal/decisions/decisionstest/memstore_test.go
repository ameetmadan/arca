package decisionstest

import "testing"

func TestMemStoreContract(t *testing.T) {
	RunContract(t, func(*testing.T) Backend { return NewMemStore() })
}
