// Package money is what an amount is (#144): a whole number of its currency's
// minor unit, as ISO 4217 says -- won for KRW, yen for JPY, cents for USD. A
// row says its currency beside it, so a workspace that begins pricing in
// another leaves what it wrote before as it was.
package money

import (
	"strconv"
	"strings"
)

// digits is the minor unit of every current ISO 4217 currency that has other
// than two digits after the point.
var digits = map[string]int{
	"BIF": 0, "CLP": 0, "DJF": 0, "GNF": 0, "ISK": 0, "JPY": 0, "KMF": 0, "KRW": 0, "PYG": 0,
	"RWF": 0, "UGX": 0, "UYI": 0, "VND": 0, "VUV": 0, "XAF": 0, "XOF": 0, "XPF": 0,
	"BHD": 3, "IQD": 3, "JOD": 3, "KWD": 3, "LYD": 3, "OMR": 3, "TND": 3,
	"CLF": 4, "UYW": 4,
}

// Digits is how many of an amount's digits in currency are after the point.
func Digits(currency string) int {
	if v, ok := digits[currency]; ok {
		return v
	}

	return 2
}

// String is an amount as a sentence tells it: "915,000 KRW", "12.50 USD". One
// that names no currency is the whole number it is.
func String(n int64, currency string) string {
	sign := ""
	if n < 0 {
		sign, n = "-", -n
	}
	v := strconv.FormatInt(n, 10)
	d := 0
	if currency != "" {
		d = Digits(currency)
	}
	frac := ""
	if d > 0 {
		if len(v) <= d {
			v = strings.Repeat("0", d-len(v)+1) + v
		}
		v, frac = v[:len(v)-d], "."+v[len(v)-d:]
	}
	var b strings.Builder
	for i, c := range v {
		if i > 0 && (len(v)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	s := sign + b.String() + frac
	if currency == "" {
		return s
	}

	return s + " " + currency
}
