package money_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/internal/money"
)

func TestAnAmountIsToldInItsCurrency(t *testing.T) {
	x := require.New(t)

	x.Equal("915,000 KRW", money.String(915000, "KRW"))
	x.Equal("12.50 USD", money.String(1250, "USD"))
	x.Equal("0.05 USD", money.String(5, "USD"))
	x.Equal("1,234.567 KWD", money.String(1234567, "KWD"))
	x.Equal("-1,000 JPY", money.String(-1000, "JPY"))
	x.Equal("100", money.String(100, ""), "a currency it names none of")
}
