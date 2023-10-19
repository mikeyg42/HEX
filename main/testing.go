package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"testing"
	"time"
	"bytes"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	hex "github.com/mikeyg42/HEX/models"
)

const Topic = "event_topic"

func prependTopicToPayload(topic string, payload []byte) []byte {
	topicBytes := []byte(topic + hex.Delimiter)
	return append(topicBytes, payload...)
}

// Mock of sending event to event bus
func sendToEventBus(data []byte) {
	// Here you'd actually send to your event bus
	fmt.Println("Sent to Event Bus:", string(data))
}

// Mock of Persister agent
func persisterAgent() {
	// Listen to event bus and get the data
	// For simplicity, I'm assuming you get the data as a byte slice
	data := []byte("event_topic#...") // your actual data

	// Separate topic and payload
	parts := bytes.Split(data, []byte(hex.Delimiter))
	topic := string(parts[0])
	payload := parts[1]

	if topic == Topic {
		var evt Event
		err := json.Unmarshal(payload, &evt)
		if err != nil {
			log.Fatalf("Failed to unmarshal event: %v", err)
		}

		// Save to PostgreSQL
		conn, err := pgx.Connect(context.Background(), "your-connection-string")
		if err != nil {
			log.Fatalf("Unable to connect to database: %v", err)
		}
		defer conn.Close(context.Background())

		_, err = conn.Exec(context.Background(), `
			INSERT INTO events (uuid, user_id, event_type, event_info, timestamp)
			VALUES ($1, $2, $3, $4, $5)
		`, evt.UUID, evt.UserID, evt.EventType, evt.EventInfo, evt.Timestamp)
		if err != nil {
			log.Fatalf("Failed to insert event: %v", err)
		}
	}
}

func waitForRecordToAppearInDB(ctx context.Context, uuid uuid.UUID) error {
	conn, err := pgx.Connect(ctx, "your-connection-string")
	if err != nil {
		return fmt.Errorf("Unable to connect to database: %w", err)
	}
	defer conn.Close(ctx)

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			var id uuid.UUID
			err = conn.QueryRow(ctx, `SELECT uuid FROM events WHERE uuid = $1`, uuid).Scan(&id)
			if err == nil {
				return nil
			}
		case <-ctx.Done():
			return fmt.Errorf("Timeout while waiting for record: %w", ctx.Err())
		}
	}
}

func TestEventPersister(t *testing.T) {
	evt := Event{
		UUID:       uuid.New(),
		UserID:     "user123",
		EventType:  "login",
		EventInfo:  "User logged in",
		Timestamp:  time.Now(),
	}

	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("Failed to marshal event: %v", err)
	}

	message := prependTopicToPayload(Topic, data)
	sendToEventBus(message)

	// Start the persister agent
	go persisterAgent()

	// Poll database for record
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = waitForRecordToAppearInDB(ctx, evt.UUID)
	if err != nil {
		t.Fatalf("Error while waiting for record: %v", err)
	}
}
