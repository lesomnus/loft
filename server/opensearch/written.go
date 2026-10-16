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

// written is what one call wrote, of each [Index], as [Recorder] heard of
// it.
type written struct {
	mu   sync.Mutex
	keys map[*Index][]uuid.UUID
}

type writtenKey struct{}

func (w *written) add(ix *Index, k uuid.UUID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.keys == nil {
		w.keys = map[*Index][]uuid.UUID{}
	}
	if !slices.Contains(w.keys[ix], k) {
		w.keys[ix] = append(w.keys[ix], k)
	}
}

// Interceptor writes the documents of what a call wrote before the call
// answers, and makes them searchable at once: somebody who saves a thing and
// then looks for it finds it, rather than a second or two later when the
// loop gets to it -- receiving a purchase, say, which shows what it received
// by searching for its tags.
//
// It is the caller's own writes, and of each the document of the row itself:
// a type or a model renamed is any number of documents of assets, and those
// wait for the loop. And it is the sooner of two ways and not the only one --
// what it could not write, the loop writes from the rows the recorder wrote
// all the same, so a cluster that is slow or down costs a call two seconds at
// most and is not its failure.
func (x Indexer) Interceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		w := &written{}
		res, err := handler(context.WithValue(ctx, writtenKey{}, w), req)
		if err != nil || len(w.keys) == 0 {
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

// writeNow writes the documents of keys to every index of their [Index] and
// refreshes them. A call that wrote more than a chunk of one leaves it to the
// loop.
func (x Indexer) writeNow(ctx context.Context, keys map[*Index][]uuid.UUID) error {
	for _, ix := range Indexes {
		ks := keys[ix]
		if len(ks) == 0 || len(ks) > chunk {
			continue
		}
		indexes, err := x.Client.indexes(ctx, ix)
		if err != nil {
			return err
		}
		if len(indexes) == 0 {
			continue
		}
		if err := x.write(ctx, ix, indexes, ks); err != nil {
			return err
		}
		for _, idx := range indexes {
			if err := x.Client.refresh(ctx, idx); err != nil {
				return err
			}
		}
	}
	return nil
}
