package opensearch

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"
	"uuid"

	"github.com/lesomnus/otx/log"
	"google.golang.org/grpc"
)

// written is the assets one call wrote, as [Recorder] heard of them.
type written struct {
	mu   sync.Mutex
	keys []uuid.UUID
}

type writtenKey struct{}

func (w *written) add(k uuid.UUID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !slices.Contains(w.keys, k) {
		w.keys = append(w.keys, k)
	}
}

// Interceptor writes the documents of the assets a call wrote before the
// call answers, and makes them searchable at once: somebody who saves a thing
// and then looks for it finds it, rather than a second or two later when the
// loop gets to it -- receiving a purchase, say, which shows what it received
// by searching for its tags.
//
// It is the caller's own writes, and assets only: a type or a model renamed
// is any number of documents, and those wait for the loop. And it is the
// sooner of two ways and not the only one -- what it could not write, the
// loop writes from the rows the recorder wrote all the same, so a cluster
// that is slow or down costs a call two seconds at most and is not its
// failure.
func (x Indexer) Interceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		w := &written{}
		res, err := handler(context.WithValue(ctx, writtenKey{}, w), req)
		if err != nil || len(w.keys) == 0 || len(w.keys) > chunk {
			return res, err
		}

		// The call's answer is not the call's to wait on for long, nor to be
		// cancelled with: it has committed.
		at, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if err := x.writeNow(at, w.keys); err != nil {
			log.From(ctx).WarnContext(ctx, "search: what a call wrote waits for the loop", slog.String("method", info.FullMethod), slog.String("error", err.Error()))
		}
		return res, nil
	}
}

// writeNow writes the documents of keys to every index of the deployment and
// refreshes them.
func (x Indexer) writeNow(ctx context.Context, keys []uuid.UUID) error {
	indexes, err := x.Client.indexes(ctx)
	if err != nil || len(indexes) == 0 {
		return err
	}
	if err := x.write(ctx, indexes, keys); err != nil {
		return err
	}
	for _, idx := range indexes {
		if err := x.Client.refresh(ctx, idx); err != nil {
			return err
		}
	}
	return nil
}
