package rescuessh

import (
	"context"
	"io"
)

func (s *Session) RunTo(ctx context.Context, w io.Writer, args ...string) error {
	return s.run(ctx, w, args)
}
