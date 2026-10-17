package broker

import (
	"context"
	"errors"
	"fmt"

	"github.com/tekmora/epitrop/internal/eventlog"
)

// Log publishes log-first: an event is appended to the event log before any subscriber
// sees it, and subscribers receive the signed eventlog.Record the log holds. A subscriber
// can therefore read back from the log whatever it was given (ADR-0004).
type Log struct {
	broker *Broker
	writer *eventlog.Writer
}

// NewLog returns a log-first publisher over b and the event log w appends to.
func NewLog(b *Broker, w *eventlog.Writer) (*Log, error) {
	if b == nil || w == nil {
		return nil, errors.New("broker: a log-first publisher needs a broker and an event-log writer")
	}
	return &Log{broker: b, writer: w}, nil
}

// Publish appends ev and delivers its record to every subscriber of topic. When a queue is
// full it returns ErrFull and appends nothing; when the append fails nobody gets a message.
// The append runs under the broker's lock, so publishes wait for it, up to the event log's
// lock timeout.
func (l *Log) Publish(ctx context.Context, topic string, ev eventlog.Event) (eventlog.Record, error) {
	var rec eventlog.Record
	err := l.broker.publishAfter(ctx, topic, func(ctx context.Context) (any, error) {
		appended, err := l.writer.Append(ctx, ev)
		if err != nil {
			return nil, fmt.Errorf("broker: append to the event log before %s: %w", topic, err)
		}
		rec = appended
		return appended, nil
	})
	if err != nil {
		return eventlog.Record{}, err
	}
	return rec, nil
}

// Subscribe adds a subscriber to topic; see Broker.Subscribe.
func (l *Log) Subscribe(topic string) (*Subscription, func()) {
	return l.broker.Subscribe(topic)
}
