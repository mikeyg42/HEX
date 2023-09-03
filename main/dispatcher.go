package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	hex "github.com/mikeyg42/HEX/models"
	storage "github.com/mikeyg42/HEX/storage"
	zap "go.uber.org/zap"
)

// Dispatcher represents the combined event and command dispatcher.
type Dispatcher struct {
	Container       *hex.GameContainer
	commandHandlers map[string]func(interface{})
	eventHandlers   map[string]func(interface{})
	CommandChan     chan interface{}
	EventChan       chan interface{}
	// to initiate a game:
	StartChan chan hex.Command
}

func NewDispatcher(ctx context.Context, container *hex.GameContainer) *Dispatcher {
	d := &Dispatcher{
		CommandChan: make(chan interface{}),
		EventChan:   make(chan interface{}),
		Container:   container,
		StartChan:   make(chan hex.Command),
	}

	//  the DONE chan controls the Dispatcher!
	done := make(chan struct{})
	errChan := make(chan error)

	// Subscribe to Redis Pub/Sub channels for commands and events
	d.subscribeToCommands(done, errChan)
	d.subscribeToEvents(done, errChan)

	return d
}

// Start starts the command and event Dispatchers.
func (d *Dispatcher) StartDispatcher(ctx context.Context) chan<- error {
	errChan := make(chan error) // create an error channel

	go d.commandDispatcher(ctx, errChan)
	go d.eventDispatcher(ctx, errChan)

	// Let's listen to our error channel now
	go func() {
		for err := range errChan {
			// Log the error or handle it appropriately.
			logger, ok := ctx.Value(hex.ErrorLoggerKey{}).(*storage.Logger)
			if !ok {
				logger.ErrorLog(ctx, "Error in Dispatcher", zap.Error(err))
			}
		}
	}()
	return errChan
}

// CommandDispatcher is FIRST STOP for a message in this channel - it dispatches the command to the appropriate channel.
func (d *Dispatcher) CommandDispatcher(cmd interface{}) {
	d.CommandChan <- cmd
}

func (d *Dispatcher) commandDispatcher(ctx context.Context, errChan chan<- error) {

	for cmd := range d.CommandChan {
		payload, err := json.Marshal(cmd)
		if err != nil {
			errChan <- err
		}

		// Publish command to Redis Pub/Sub channel

		err = d.Container.Persister.Redis.Client.Publish(ctx, "commands", payload).Err()
		if err != nil {
			errChan <- err
		}
	}
}

// EventDispatcher dispatches the event to the appropriate channel.
func (d *Dispatcher) EventDispatcher(event interface{}) {
	d.EventChan <- event
}

func (d *Dispatcher) eventDispatcher(ctx context.Context, errChan chan<- error) {
	for event := range d.EventChan {

		payload, err := json.Marshal(event)
		if err != nil {
			errChan <- err
		}

		// Publish event to Redis Pub/Sub channel
		err = d.Container.Persister.Redis.Client.Publish(ctx, "events", payload).Err()
		if err != nil {
			errChan <- err
		}
	}
}

// Subscribe to commands and events with the done channel
// subscribeToCommands with error channel
func (d *Dispatcher) subscribeToCommands(done <-chan struct{}, errChan chan<- error) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	commandPubSub := d.Container.Persister.Redis.Client.Subscribe(ctx, "commands")
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
				cmdType, found := hex.CmdTypeMap[msg.Channel]
				if !found {
					err := fmt.Errorf("unknown command type: %s", cmdType.(string))
					d.Container.ErrorLog.InfoLog(ctx, "unknown command type", zap.String("cmdType", cmdType.(string)))
					errChan <- err
					continue
				}

				cmdValue := reflect.New(reflect.TypeOf(cmdType).Elem()).Interface()
				err := json.Unmarshal([]byte(msg.Payload), &cmdValue)
				if err != nil {
					err = fmt.Errorf("Error unmarshaling %s: %v\n", msg.Channel, err)
					d.Container.ErrorLog.ErrorLog(ctx, err.Error(), zap.Error(err))
					errChan <- err
					continue
				}
				// Type assertion to ensure that the unmarshaled event implements the Command interface
				cmdTypeAsserted, ok := cmdValue.(hex.Command)
				if !ok {
					err := fmt.Errorf("unmarshaled command does not implement Event interface")
					d.Container.ErrorLog.InfoLog(ctx, err.Error(), zap.Error(err))
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
	eventPubSub := d.Container.Persister.Redis.Client.Subscribe(ctx, "events")
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
				eventType, found := hex.EventTypeMap[msg.Channel]
				if !found {
					err := fmt.Errorf("unknown event type: %s", msg.Channel)
					d.Container.ErrorLog.InfoLog(ctx, err.Error())
					errChan <- err
					continue
				}

				eventValue := reflect.New(reflect.TypeOf(eventType).Elem()).Interface()
				err := json.Unmarshal([]byte(msg.Payload), &eventValue)
				if err != nil {
					err = fmt.Errorf("Error unmarshaling %s: %v\n", msg.Channel, err)
					d.Container.ErrorLog.ErrorLog(ctx, err.Error(), zap.Error(err))
					errChan <- err
					continue
				}

				// Type assertion to ensure that the unmarshaled event implements the Event interface
				evtTypeAsserted, ok := eventValue.(hex.Event)
				if !ok {
					err := fmt.Errorf("unmarshaled event does not implement Event interface")
					d.Container.ErrorLog.InfoLog(ctx, err.Error(), zap.Error(nil))
					errChan <- err
					continue
				}

				d.handleEvent(done, evtTypeAsserted, eventPubSub)
			}
		}
	}()
}
