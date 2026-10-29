package kittest

import (
	"os"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	settleBudget = time.Second
	os.Exit(m.Run())
}
