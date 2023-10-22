package main

import (
	hex "github.com/mikeyg42/HEX/models"
	"context"
	"encoding/json"
	"time"
)

type Lobby struct {
	Players            []hex.Player
	MatchmakingService chan []byte            // the lobby receives on this channel from the matchmaking service
	PlayerChans        map[string]chan []byte // the lobby broadcasts on these channels to all players
}

func MatchmakingLoop(l *Lobby, ctx context.Context) {
	for {
		select {
			case <-ctx.Done():
				return
			case match := <-l.MatchmakingService:

				// broadcast the match to all players
				twoPlayers := l.PublishPairing(match)

				// Wait for acks from both players
				ctx2, cancel := context.WithTimeout(ctx, time.Until(time.Now().Add(5*time.Second)))
				defer cancel()
				for {
					select {
					case <-ctx2.Done():
						// handle timeout
						return
					case :	// players will need to send acknowleding events to the lobby by way of their player chans 
					
					//??
				
					}
				}
			}
			l.LockPairIntoMatch(twoPlayers)
		}
	}
}

func (l *Lobby) PublishPairing(match []byte) [2]string {
	var playerIDs [2]string
	err := json.Unmarshal(match, &playerIDs)
	if err != nil {
		// handle error
		panic(err)
	}

	announcePair := hex.LobbyEvent{
		data:       playerIDs,
		originator: "MatchmakingService",
		timeStamp:  time.Now(),
	}

	jsonPair, err := json.Marshal(announcePair)
	if err != nil {
		// handle error
		panic(err)
	}

	for _, eachPlayer := range l.PlayerChans {
		eachPlayer <- jsonPair
	}

	return playerIDs
}
