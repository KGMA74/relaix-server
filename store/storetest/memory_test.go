package storetest_test

import (
	"testing"

	"github.com/KGMA74/relaix-server/store"
	"github.com/KGMA74/relaix-server/store/storetest"
)

// The fake must satisfy the same contract as the real store, or the tests that
// lean on it are measuring the fake's imagination.
func TestMemoryStoreConformance(t *testing.T) {
	storetest.RunConformance(t, func(t *testing.T) store.Store {
		return storetest.New()
	})
}
