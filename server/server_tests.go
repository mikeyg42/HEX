package server

import (
	"testing"
	"os"
	"nhooyr.io/websocket"

)

func TestDefinePooledConnectionsHappyPath(t *testing.T) {
	os.Setenv("DATABASE_DSN", "your_correct_DSN_string_here")
	_, err := websocket.definePooledConnections()
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
}
