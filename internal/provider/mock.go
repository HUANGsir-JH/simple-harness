package provider

import (
	"context"
	"errors"
)

// FakeStream is a scripted EventStream used by tests (including the agent
// package tests). It lives in a non-test file so other packages can reuse it.
type FakeStream struct {
	events []Event
	idx    int
	err    error
}

// NewFakeStream builds a FakeStream from a fixed event list.
func NewFakeStream(events []Event) *FakeStream { return &FakeStream{events: events} }

func (f *FakeStream) Next() bool {
	if f.idx < len(f.events) {
		f.idx++
		return true
	}
	return false
}

func (f *FakeStream) Current() Event {
	if f.idx == 0 || f.idx > len(f.events) {
		return Event{Type: EventError, Error: errors.New("Current called before Next")}
	}
	return f.events[f.idx-1]
}

func (f *FakeStream) Err() error   { return f.err }
func (f *FakeStream) Close() error { return nil }

// FakeClient is a scripted Client used by tests. It records the last request
// for assertion and returns the configured stream.
type FakeClient struct {
	StreamFn func(ctx context.Context, req Request) (EventStream, error)
	LastReq  *Request
}

func (f *FakeClient) Stream(ctx context.Context, req Request) (EventStream, error) {
	f.LastReq = &req
	if f.StreamFn == nil {
		return NewFakeStream(nil), nil
	}
	return f.StreamFn(ctx, req)
}

// Ensure test doubles satisfy the public interfaces.
var (
	_ EventStream = (*FakeStream)(nil)
	_ Client      = (*FakeClient)(nil)
)
