//go:build integration

package integration

import (
	"log"
	"os"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	env := loadTestEnv()
	log.Println("Waiting for BMC to be ready...")
	if err := waitForBMCReady(env, 30*time.Second); err != nil {
		log.Fatalf("BMC failed to become ready: %v", err)
	}
	log.Println("BMC is ready, running tests...")
	os.Exit(m.Run())
}
