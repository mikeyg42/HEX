package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"

	hex "github.com/mikeyg42/HEX/models"
)

const topicCodeLength = 5 // fixed legth of the topic code

// move this to the main MAIN funtion
func main() {
	err := runWebsocketServer("localhost:8080")
	if err != nil {
		panic(err)
	}

}

// run initializes the lobbyServer and starts the HTTP server.
func runWebsocketServer(tcpAddr string) error {
	mainCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listener, err := net.Listen("tcp", tcpAddr)
	if err != nil {
		return err
	}
	log.Printf("listening on http://%v", listener.Addr())

	// Create a new HTTP server and set the base context to our main context.
	server := &http.Server{
		Handler: newChatServer(),
		BaseContext: func(_ net.Listener) context.Context {
			return mainCtx // each request's context r.Context() to be derived from our main context.
		},
		ReadTimeout:  time.Second * 10,
		WriteTimeout: time.Second * 10,
		// set the max read and write too??
	}

	// make error channel to consolidate for errors from the server
	egroup, eGroupCtx := errgroup.WithContext(mainCtx)

	// Goroutine for serving HTTP
	egroup.Go(func() error {
		return server.Serve(listener)
	})

	// Goroutine to handle shutdown signals
	s:= &myServer{server: server}
	egroup.Go(s.ShutdownServer(eGroupCtx, mainCtx))

	// Wait for all goroutines in the errgroup
	if err := egroup.Wait(); err != nil && err != http.ErrServerClosed {
		log.Printf("failed to serve: %v", err)
		return err
	}

	return nil
}

// lobbyServer enables broadcasting to a set of subscriberserver.
type lobbyServer struct {
	// max number of eventMsgs that can be queued for a subscriber before it is lost. Defaults to 16.
	subscriberEventMsgBuffer int

	// publishLimiter controls the rate limit applied to the publish endpoint.
	// Defaults to one publish every 100ms with a burst of 8.
	publishLimiter *rate.Limiter

	// where logs are sent.
	logf func(f string, v ...interface{})

	// serveMux routes the various endpoints to the appropriate handler.
	serveMux http.ServeMux

	subscribersMu sync.Mutex
	subscribers   map[*subscriber]struct{} // struct contains the events channel and a func to call if they "cant hang"
}

type myServer struct {
	server *http.Server
}

func (s *myServer)ShutdownServer(eGroupCtx, mainCtx context.Context) error {
	for{ 
		select {
		case <-eGroupCtx.Done():
			return eGroupCtx.Err()
		case <-mainCtx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second*10)				
			defer cancel()

			// Trigger graceful server shutdown - give it 10 seconds to try to finish
			if err := s.server.Shutdown(shutdownCtx); err != nil {
				return err
			}
			log.Println("server gracefully shut down")
			return nil
		}
	}
}


// newChatServer constructs a lobbyServer with the defaultserver.
func newChatServer() *lobbyServer {
	lobbyServ := &lobbyServer{
		subscriberEventMsgBuffer: 16,
		logf:                     log.Printf,
		subscribers:              make(map[*subscriber]struct{}),
		publishLimiter:           rate.NewLimiter(rate.Every(time.Millisecond*100), 8),
	}
	lobbyServ.serveMux.Handle("/", http.FileServer(http.Dir(".")))
	lobbyServ.serveMux.HandleFunc("/subscribe", lobbyServ.subscribeHandler)
	lobbyServ.serveMux.HandleFunc("/publish", lobbyServ.publishHandler)

	return lobbyServ
}

// EventMsgs are sent on the EventMessage channel and .
type subscriber struct {
	evts         chan []byte // channel to receive dispatches from eventBus
	closeTooSlow func()      // if the client cannot keep up with the eventMsgs, closeTooSlow is called
}

func (lobbyServ *lobbyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	lobbyServ.serveMux.ServeHTTP(w, r)
}

// subscribeHandler accepts the WebSocket connection and then subscribes
// it to all future eventMsgserver.
func (lobbyServ *lobbyServer) subscribeHandler(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		lobbyServ.logf("%v", err)
		return
	}
	defer c.Close(websocket.StatusInternalError, "")

	err = lobbyServ.subscribe(r.Context(), c)
	if errors.Is(err, context.Canceled) {
		return
	}
	if websocket.CloseStatus(err) == websocket.StatusNormalClosure ||
		websocket.CloseStatus(err) == websocket.StatusGoingAway {
		return
	}
	if err != nil {
		lobbyServ.logf("%v", err)
		return
	}
}

// publishHandler reads the request body with a limit of 8192 bytes and then publishes
// the received eventMsg.
func (lobbyServ *lobbyServer) publishHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	body := http.MaxBytesReader(w, r.Body, 8192)
	evt, err := ioutil.ReadAll(body)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
		return
	}

	lobbyServ.publish(evt)

	w.WriteHeader(http.StatusAccepted)
}

// creates a subscriber
func (lobbyServ *lobbyServer) subscribe(ctx context.Context, c *websocket.Conn) error {
	ctx = websocket.c.CloseRead(ctx)
	// this fcn closeRead keeps reading from the connection for a while to process pings and stuff

	s := &subscriber{
		evts: make(chan []byte, lobbyServ.subscriberEventMsgBuffer),
		closeTooSlow: func() {
			c.Close(websocket.StatusPolicyViolation, "connection too slow to keep up with eventMsgs")
		},
	}
	lobbyServ.AddSubscriber(s) // adds the subscriber to the map
	defer lobbyServ.DeleteSubscriber(s)

	for {
		select {
		case evt := <-s.evts:
			err := writeTimeout(ctx, time.Second*5, c, evt)
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// publish publishes an evt to all subscribers. It never blocks and so eventMsgs to slow subscribers are dropped.
func (lobbyServ *lobbyServer) publish(evt []byte) {
	lobbyServ.subscribersMu.Lock()
	defer lobbyServ.subscribersMu.Unlock()

	lobbyServ.publishLimiter.Wait(context.Background())

	for s := range lobbyServ.subscribers {
		select {
		case s.evts <- evt:
		default:
			go s.closeTooSlow()
		}
	}
}

// AddSubscriber registers a subscriber into the map.
func (lobbyServ *lobbyServer) AddSubscriber(s *subscriber) {
	lobbyServ.subscribersMu.Lock()
	lobbyServ.subscribers[s] = struct{}{}
	lobbyServ.subscribersMu.Unlock()
}

// removes a subscriber from the map of subscribers
func (lobbyServ *lobbyServer) DeleteSubscriber(s *subscriber) {
	lobbyServ.subscribersMu.Lock()
	delete(lobbyServ.subscribers, s)
	lobbyServ.subscribersMu.Unlock()
}

func writeTimeout(ctx context.Context, timeout time.Duration, c *websocket.Conn, msg []byte) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return c.Write(ctx, websocket.EventMsgText, msg)
}

func ReadFromWebsocket(ctx context.Context, c *websocket.Conn) (string, []byte, error) {
	msg := make([]byte, 0, 1024)
	err := wsjson.Read(ctx, c, &msg)
	if err != nil {
		return "", nil, err
	}

	// Ensure you've read enough bytes for the topic.
	if len(msg) < topicCodeLength+2 {
		return "", nil, errors.New("message too short to contain topic")
	}

	// Extract topic and return
	topic := string(msg[:topicCodeLength])
	return topic, msg[topicCodeLength+1:], nil
}

func WriteToWebsocket(ctx context.Context, c *websocket.Conn, msg []byte) error {
	yn_json := json.Valid([]byte(msg))
	if yn_json {
		err := wsjson.Write(ctx, c, msg)
		return err
	}

	return fmt.Errorf("message is not valid json")
}

func handleWebsocketConnection(ctx context.Context, c *websocket.Conn, geb *hex.GameEventBus) {
	for {
		msg, _, err := ReadFromWebsocket(ctx, c)
		if err != nil {
			// Handle read error.
			return
		}

        // Here, you can prepend the topic.
        topic := "YOUR_TOPIC" // You need to determine how to set the topic.
        msgWithTopic := prependTopicToPayload(topic, msg)

        geb.DispatchMessage(string(msgWithTopic))

	}
}


func prependTopicToPayload(topic string, payload []byte) []byte {
	topicBytes := []byte(topic + hex.Delimiter)
	return append(topicBytes, payload...)
}
