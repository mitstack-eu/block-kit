package contract

import "context"

type appDeletingKey struct{}

func WithAppDeleting(ctx context.Context) context.Context {
	return context.WithValue(ctx, appDeletingKey{}, true)
}

// AppDeleting is the one test a kit helper reads the app's delete by, true on a context WithAppDeleting marked.
func AppDeleting(ctx context.Context) bool {
	marked, _ := ctx.Value(appDeletingKey{}).(bool)
	return marked
}
