package pubsub

import (
	"time"
)

type MetaData map[string]interface{}

type Event struct {
	metaData  MetaData
	timestamp time.Time
}

type Eventer interface {
	MetaData() MetaData
	Timestamp() time.Time
	AddMetaData(key string, value interface{})
}

func NewEvent() Event {
	return Event{
		metaData:  make(MetaData),
		timestamp: time.Now(),
	}
}

func (evt *Event) MetaData() MetaData {
	return evt.metaData
}

func (evt *Event) Timestamp() time.Time {
	return evt.timestamp
}

func (evt *Event) AddMetaData(key string, value interface{}) {
	evt.metaData[key] = value
}
