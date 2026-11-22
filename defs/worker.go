package defs

import "context"

type workerKey struct{}

// WithWorker marks the requests made with ctx as sent by download or upload
// worker i. The transport gives each worker its own connection, so the
// workers do not share one under HTTP/2 or HTTP/3.
func WithWorker(ctx context.Context, i int) context.Context {
	return context.WithValue(ctx, workerKey{}, i)
}

// Worker returns the worker set by WithWorker, if any.
func Worker(ctx context.Context) (int, bool) {
	i, ok := ctx.Value(workerKey{}).(int)
	return i, ok
}
