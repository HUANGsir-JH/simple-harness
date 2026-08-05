package provider

import (
	"context"
	"errors"
)

// FakeStream 是测试用的脚本化 EventStream（包括 agent 包测试）。
// 放在非 _test 文件中，便于其它包复用。
type FakeStream struct {
	events []Event
	idx    int
	err    error
}

// NewFakeStream 用固定事件列表构造 FakeStream。
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

// FakeClient 是测试用的脚本化 Client。它记录最后一次请求以供断言，
// 并返回配置好的流。
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

// 确保测试替身满足公开接口。
var (
	_ EventStream = (*FakeStream)(nil)
	_ Client      = (*FakeClient)(nil)
)
