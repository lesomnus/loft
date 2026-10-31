package domain

import (
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
)

// TestStockMovedOutsideAnOperationIsAMistake: every operation that moves stock
// has recorded its event, which the movement points at, before it moves any;
// one with no event to point at is a mistake here, refused before anything is
// read or written -- this transaction has no database to write to.
func TestStockMovedOutsideAnOperationIsAMistake(t *testing.T) {
	err := (&Tx{}).moveStock(app.Stock_builder{}.Build(), 1, "receive", pdid.Nil, time.Now())
	require.Equal(t, codes.Internal, status.Code(err), "%v", err)
}
