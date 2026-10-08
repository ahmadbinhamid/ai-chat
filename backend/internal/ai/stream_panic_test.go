package ai

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
)

// panickingDecoder stands in for a provider stream whose reader panics mid-read.
type panickingDecoder struct{}

func (panickingDecoder) Event() ssestream.Event { return ssestream.Event{} }
func (panickingDecoder) Next() bool             { panic("decoder blew up") }
func (panickingDecoder) Close() error           { return nil }
func (panickingDecoder) Err() error             { return nil }

func TestConsumeStream_ReaderPanicFailsTheCallInsteadOfHanging(t *testing.T) {
	stream := ssestream.NewStream[anthropic.MessageStreamEventUnion](panickingDecoder{}, nil)
	var message anthropic.Message
	done := make(chan error, 1)
	go func() { done <- consumeStream(context.Background(), stream, &message, time.Minute, time.Minute) }()

	select {
	case err := <-done:
		if !errors.Is(err, errStreamReaderPanicked) {
			t.Errorf("want errStreamReaderPanicked, got %v", err)
		}
		if isRetryableStreamErr(err) {
			t.Error("a panicked reader must not be retried")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("consumeStream hung after the reader panicked")
	}
}
