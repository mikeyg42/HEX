package server

import (
	"testing"
	"fmt"
	"nhood"
	"nhooyr.io/websocket"

)

func TestDefinePooledConnectionsHappyPath(t *testing.T) {
	os.Setenv("DATABASE_DSN", "your_correct_DSN_string_here")
	_, err := definePooledConnections()
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
}
