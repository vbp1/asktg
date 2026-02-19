package tasks

import "context"

type Queue interface {
	Enqueue(ctx context.Context, taskType string, payload string, priority int) error
	Pause() error
	Resume() error
}

type StubQueue struct{}

func NewStubQueue() *StubQueue { return &StubQueue{} }

func (q *StubQueue) Enqueue(context.Context, string, string, int) error { return nil }
func (q *StubQueue) Pause() error                                       { return nil }
func (q *StubQueue) Resume() error                                      { return nil }
