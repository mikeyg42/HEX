package main

import (
	"context"
	"errors"
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
)

// move this to the main MAIN funtion
func main() {
	err := runWebsocketServer("localhost:8080")
	if err != nil {
		panic(err)
	}
}

// run initializes the lobbyServer and then
// starts a http.Server for the passed in addresserver.
func runWebsocketServer(tcpAddr string) error {
	mainCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listener, err := net.Listen("tcp", tcpAddr)
	if err != nil {
		return err
	}
	log.Printf("listening on http://%v", listener.Addr())

	cs := newChatServer()
	server := &http.Server{
		Handler: cs,
		BaseContext: func(_ net.Listener) context.Context {
			return mainCtx // each request's context r.Context() to be derived from our main context.
		},
		ReadTimeout:  time.Second * 10,
		WriteTimeout: time.Second * 10,
	}

	egroup, eGroupCtx := errgroup.WithContext(mainCtx)

	// Goroutine for serving HTTP
	egroup.Go(func() error {
		return server.Serve(listener)
	})

	// Goroutine to handle shutdown signals
	egroup.Go(func() error {
		select {
		case <-eGroupCtx.Done():
			return eGroupCtx.Err()
		case <-mainCtx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second*10)
			defer cancel()

			// Trigger graceful shutdown
			if err := server.Shutdown(shutdownCtx); err != nil {
				return err
			}
			log.Println("server gracefully shut down")
		}
		return nil
	})

	// Wait for all goroutines in the errgroup
	if err := egroup.Wait(); err != nil && err != http.ErrServerClosed {
		log.Printf("failed to serve: %v", err)
		return err
	}

	return nil
}

// lobbyServer enables broadcasting to a set of subscriberserver.
type lobbyServer struct {
	// subscriberEventMsgBuffer controls the max number
	// of eventMsgs that can be queued for a subscriber
	// before it is kicked.
	//
	// Defaults to 16.
	subscriberEventMsgBuffer int

	// publishLimiter controls the rate limit applied to the publish endpoint.
	//
	// Defaults to one publish every 100ms with a burst of 8.
	publishLimiter *rate.Limiter

	// logf controls where logs are sent.
	// Defaults to log.Printf.
	logf func(f string, v ...interface{})

	// serveMux routes the various endpoints to the appropriate handler.
	serveMux http.ServeMux

	subscribersMu sync.Mutex
	subscribers   map[*subscriber]struct{}
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

// subscriber represents a subscriber.
// EventMsgs are sent on the EventMessage channel and if the client
// cannot keep up with the eventMsgs, closeSlow is called.
type subscriber struct {
	msgs      chan []byte
	closeSlow func()
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

// subscribe subscribes the given WebSocket to all broadcast eventMsgserver.
// It creates a subscriber with a buffered msgs chan to give some room to slower
// connections and then registers the subscriber. It then listens for all eventMsgs
// and writes them to the WebSocket. If the context is cancelled or
// an error occurs, it returns and deletes the subscription.
//
// It uses CloseRead to keep reading from the connection to process control
// eventMsgs and cancel the context if the connection dropserver.
func (lobbyServ *lobbyServer) subscribe(ctx context.Context, c *websocket.Conn) error {
	ctx = c.CloseRead(ctx)

	s := &subscriber{
		msgs: make(chan []byte, lobbyServ.subscriberEventMsgBuffer),
		closeSlow: func() {
			c.Close(websocket.StatusPolicyViolation, "connection too slow to keep up with eventMsgs")
		},
	}
	lobbyServ.addSubscriber(s)
	defer lobbyServ.deleteSubscriber(s)

	for {
		select {
		case evt := <-s.msgs:
			err := writeTimeout(ctx, time.Second*5, c, evt)
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// publish publishes the msg to all subscriberserver.
// It never blocks and so eventMsgs to slow subscribers are dropped.
func (lobbyServ *lobbyServer) publish(msg []byte) {
	lobbyServ.subscribersMu.Lock()
	defer lobbyServ.subscribersMu.Unlock()

	lobbyServ.publishLimiter.Wait(context.Background())

	for s := range lobbyServ.subscribers {
		select {
		case s.msgs <- msg:
		default:
			go s.closeSlow()
		}
	}
}

// addSubscriber registers a subscriber.
func (lobbyServ *lobbyServer) addSubscriber(s *subscriber) {
	lobbyServ.subscribersMu.Lock()
	lobbyServ.subscribers[s] = struct{}{}
	lobbyServ.subscribersMu.Unlock()
}

// deleteSubscriber deletes the given subscriber.
func (lobbyServ *lobbyServer) deleteSubscriber(s *subscriber) {
	lobbyServ.subscribersMu.Lock()
	delete(lobbyServ.subscribers, s)
	lobbyServ.subscribersMu.Unlock()
}

func writeTimeout(ctx context.Context, timeout time.Duration, c *websocket.Conn, msg []byte) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return c.Write(ctx, websocket.EventMsgText, msg)
}
