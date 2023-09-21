package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	hex "github.com/mikeyg42/HEX/models"
	pubsub "github.com/mikeyg42/HEX/pubsub"
	timerpkg "github.com/mikeyg42/HEX/timerpkg"
	zap "go.uber.org/zap"
)

// Dispatcher represents the combined event and command dispatcher.
type Dispatcher struct {
	Container *hex.Container // GETTING RID OF THIS SLOWLY
	EventChan chan interface{}
	// to initiate a game:
	StartChan chan pubsub.Event
	// to give handlers access to timer:
	Timer   *timerpkg.TimerControl
	PG      *hex.PostgresGameState
	RS      *hex.RedisGameState
	ErrChan chan error
	Logger  hex.LogController
}

// START-CHAN IS UNUSED!!!!

func NewDispatcher(ctx context.Context, container *hex.Container) (*Dispatcher, chan struct{}) {
	d := &Dispatcher{
		EventChan: make(chan interface{}),
		Container: container,
		StartChan: make(chan pubsub.Event),
		PG:        container.Persister.Postgres,
		RS:        container.Persister.Redis,
		ErrChan:   make(chan error),
		Logger:   container.ErrorLog,
	}

	//  the DONE chan controls the Dispatcher!
	done := make(chan struct{})

	// Subscribe to Redis Pub/Sub channels for commands and events
	d.subscribeToEvents(done)

	return d, done
}

// Start starts the command and event Dispatchers.
func (d *Dispatcher) StartDispatcher(ctx context.Context, logger hex.LogController) chan<- error {
	errChan := make(chan error) // create an error channel

	go d.eventDispatcher(ctx, errChan)

	// Let's listen to our error channel now
	go func() {
		for err := range errChan {
			// Log the error or handle it appropriately.
			logger.ErrorLog(ctx, "Error sent to errorChan in Dispatcher", zap.Error(err))
		}
	}()
	return errChan
}

// EventDispatcher dispatches the event to the appropriate channel.
func (d *Dispatcher) EventDispatcher(event interface{}) {
	d.EventChan <- event
}

func (d *Dispatcher) eventDispatcher(ctx context.Context, errChan chan<- error) {
	for event := range d.EventChan {

		payload, err := json.Marshal(event)
		if err != nil {
			d.ErrChan <- err
		}

		// Publish event to Redis Pub/Sub channel
		err = d.RS.Client.Publish(ctx, "events", payload).Err()
		if err != nil {
			d.ErrChan <- err
		}
	}
}

// subscribeToEvents with error channel
func (d *Dispatcher) subscribeToEvents(done <-chan struct{}) {
	// use a cancelable context that will end when the goroutine ends automatically (or earlier)
	ctx, cancelFunc := context.WithCancel(context.Background())

	// subscribe to the events channel (not the same as recieving though!)
	eventPubSub := d.RS.Client.Subscribe(ctx, "events")
	defer eventPubSub.Close()
	defer cancelFunc()

	// Event handler
	go func() {
		eventCh := eventPubSub.Channel() // setting up this channel allows you to actually receive what you've subscribed to
		for {
			select {
			case <-done:
				eventPubSub.Close()
				return
			case msg := <-eventCh:
				eventType, found := hex.EventTypeMap[msg.Channel]
				if !found {
					err := fmt.Errorf("unknown event type: %s", msg.Channel)
					d.ErrChan <- err
					continue
				}

				eventValue := reflect.New(reflect.TypeOf(eventType).Elem()).Interface()
				err := json.Unmarshal([]byte(msg.Payload), &eventValue)
				if err != nil {
					err = fmt.Errorf("Error unmarshaling %s: %v\n", msg.Channel, err)
					d.ErrChan <- err
					continue
				}

				// Type assertion to ensure that the unmarshaled event implements the Event interface
				evtTypeAsserted, ok := eventValue.(pubsub.Eventer)
				if !ok {
					err := fmt.Errorf("unmarshaled event does not implement Event interface")
					d.ErrChan <- err
					continue
				}

				d.handleEvent(done, evtTypeAsserted, eventPubSub)
			}
		}
	}()
}
