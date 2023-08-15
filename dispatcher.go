package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	zap "go.uber.org/zap"
)

// NewDispatcher creates a new Dispatcher instance.
func NewDispatcher(ctx context.Context, gsp *GameStatePersister, errorLogger Logger, eventCmdLogger Logger) *Dispatcher {
	// Create a new Dispatcher instance

	d := &Dispatcher{
		commandChan:    make(chan interface{}),
		eventChan:      make(chan interface{}),
		errorLogger:    errorLogger,
		eventCmdLogger: eventCmdLogger,
		persister:      gsp,
	}

	//  the DONE chan controls the Dispatcher! closing it will stop the dispatcher and both pubsubs
	done := make(chan struct{})
	errChan := make(chan error)

	// Subscribe to Redis Pub/Sub channels for commands and events
	d.subscribeToCommands(done, errChan)
	d.subscribeToEvents(done, errChan)

	return d
}

// Start starts the command and event dispatchers.
func (d *Dispatcher) StartDispatcher(ctx context.Context) chan<- error {
	errChan := make(chan error) // create an error channel

	go d.commandDispatcher(ctx, errChan)
	go d.eventDispatcher(ctx, errChan)

	// Let's listen to our error channel now
	go func() {
		for err := range errChan {
			// Log the error or handle it appropriately.
			logger, ok := ctx.Value(errorLoggerKey{}).(Logger)
			if !ok {
				logger.ErrorLog(ctx, "Error in dispatcher", zap.Error(err))
			}
		}
	}()
	return errChan
}

// CommandDispatcher is FIRST STOP for a message in this channel - it dispatches the command to the appropriate channel.
func (d *Dispatcher) CommandDispatcher(cmd interface{}) {
	d.commandChan <- cmd
}

func (d *Dispatcher) commandDispatcher(ctx context.Context, errChan chan<- error) {

	for cmd := range d.commandChan {
		payload, err := json.Marshal(cmd)
		if err != nil {
			errChan <- err
		}

		// Publish command to Redis Pub/Sub channel
		err = d.persister.redis.client.Publish(ctx, "commands", payload).Err()
		if err != nil {
			errChan <- err
		}
	}
}

// EventDispatcher dispatches the event to the appropriate channel.
func (d *Dispatcher) EventDispatcher(event interface{}) {
	d.eventChan <- event
}

func (d *Dispatcher) eventDispatcher(ctx context.Context, errChan chan<- error) {
	for event := range d.eventChan {

		payload, err := json.Marshal(event)
		if err != nil {
			errChan <- err
		}

		// Publish event to Redis Pub/Sub channel
		err = d.persister.redis.client.Publish(ctx, "events", payload).Err()
		if err != nil {
			errChan <- err
		}
	}
}

// Subscribe to commands and events with the done channel
// subscribeToCommands with error channel
func (d *Dispatcher) subscribeToCommands(done <-chan struct{}, errChan chan<- error) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	commandPubSub := d.persister.redis.client.Subscribe(ctx, "commands")
	defer cancelFunc()
	defer commandPubSub.Close()

	// Command receiver/handler
	go func() {
		cmdCh := commandPubSub.Channel()
		for {
			select {
			case <-done:
				commandPubSub.Close()
				return
			case msg := <-cmdCh:
				cmdType, found := cmdTypeMap[msg.Channel]
				if !found {
					err := fmt.Errorf("unknown command type: %s", cmdType.(string))
					d.errorLogger.InfoLog(ctx, "unknown command type", zap.String("cmdType", cmdType.(string)))
					errChan <- err
					continue
				}

				cmdValue := reflect.New(reflect.TypeOf(cmdType).Elem()).Interface()
				err := json.Unmarshal([]byte(msg.Payload), &cmdValue)
				if err != nil {
					err = fmt.Errorf("Error unmarshaling %s: %v\n", msg.Channel, err)
					d.errorLogger.ErrorLog(ctx, err.Error(), zap.Error(err))
					errChan <- err
					continue
				}
				// Type assertion to ensure that the unmarshaled event implements the Command interface
				cmdTypeAsserted, ok := cmdValue.(Command)
				if !ok {
					err := fmt.Errorf("unmarshaled command does not implement Event interface")
					d.errorLogger.InfoLog(ctx, err.Error(), zap.Error(err))
					errChan <- err
					continue
				}
				d.handleCommand(done, cmdTypeAsserted, commandPubSub)
			}
		}
	}()
}

// subscribeToEvents with error channel
func (d *Dispatcher) subscribeToEvents(done <-chan struct{}, errChan chan<- error) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	eventPubSub := d.persister.redis.client.Subscribe(ctx, "events")
	defer eventPubSub.Close()
	defer cancelFunc()

	// Event handler
	go func() {
		eventCh := eventPubSub.Channel()
		for {
			select {
			case <-done:
				eventPubSub.Close()
				return
			case msg := <-eventCh:
				eventType, found := eventTypeMap[msg.Channel]
				if !found {
					err := fmt.Errorf("unknown event type: %s", msg.Channel)
					d.errorLogger.InfoLog(ctx, err.Error())
					errChan <- err
					continue
				}

				eventValue := reflect.New(reflect.TypeOf(eventType).Elem()).Interface()
				err := json.Unmarshal([]byte(msg.Payload), &eventValue)
				if err != nil {
					err = fmt.Errorf("Error unmarshaling %s: %v\n", msg.Channel, err)
					d.errorLogger.ErrorLog(ctx, err.Error(), zap.Error(err))
					errChan <- err
					continue
				}

				// Type assertion to ensure that the unmarshaled event implements the Event interface
				evtTypeAsserted, ok := eventValue.(Event)
				if !ok {
					err := fmt.Errorf("unmarshaled event does not implement Event interface")
					d.errorLogger.InfoLog(ctx, err.Error(), zap.Error(nil))
					errChan <- err
					continue
				}

				d.handleEvent(done, evtTypeAsserted, eventPubSub)
			}
		}
	}()
}
