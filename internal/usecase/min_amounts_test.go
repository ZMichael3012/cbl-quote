package usecase_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cbl/cbl-quote/internal/usecase"
)

func TestParseMinAmounts(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		raw     string
		want    map[string]usecase.MinAmount
		wantErr bool
	}{
		"empty disables the feature":  {"", nil, false},
		"whitespace only is an error": {"   ", nil, true},
		"single pair":                 {"USDT/RUB@RUB:100000", map[string]usecase.MinAmount{"USDT/RUB": {Asset: "RUB", Minor: 100_000}}, false},
		"multiple pairs with spaces":  {" USDT/RUB@RUB:100000, BTC/USDT@BTC:5000 ", map[string]usecase.MinAmount{"USDT/RUB": {Asset: "RUB", Minor: 100_000}, "BTC/USDT": {Asset: "BTC", Minor: 5_000}}, false},
		"lowercase folds":             {"usdt/rub@rub:100", map[string]usecase.MinAmount{"USDT/RUB": {Asset: "RUB", Minor: 100}}, false},
		"identical pair is allowed":   {"USDT/USD@USDT:100", map[string]usecase.MinAmount{"USDT/USD": {Asset: "USDT", Minor: 100}}, false},
		"missing asset":               {"USDT/RUB:100000", nil, true},
		"asset outside the pair":      {"USDT/RUB@BTC:100", nil, true},
		"missing amount":              {"USDT/RUB@RUB", nil, true},
		"zero amount":                 {"USDT/RUB@RUB:0", nil, true},
		"negative amount":             {"USDT/RUB@RUB:-1", nil, true},
		"bad pair shape":              {"USDT@RUB:100", nil, true},
		"duplicate pair":              {"USDT/RUB@RUB:100,usdt/rub@rub:200", nil, true},
	}
	for name, tc := range cases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := usecase.ParseMinAmounts(tc.raw)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
