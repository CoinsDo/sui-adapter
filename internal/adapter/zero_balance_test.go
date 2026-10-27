package adapter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	rpcv2 "sui-adapter/internal/suiv2"
)

// Exercise real RPC serialization for every combination of absent, zero and
// positive components. Missing total must never become a fabricated zero.
func TestZeroBalancePresenceAcrossMethods(t *testing.T) {
	values := []*uint64{nil, ptr(uint64(0)), ptr(uint64(1))}
	for _, method := range []string{"suix_getBalance", "suix_listBalances", "suix_getAllBalances"} {
		for ti, total := range values {
			for ai, address := range values {
				for ci, coin := range values {
					t.Run(fmt.Sprintf("%s/total%d/address%d/coin%d", method, ti, ai, ci), func(t *testing.T) {
						balance := &rpcv2.Balance{CoinType: ptr(defaultCoinType), Balance: total, AddressBalance: address, CoinBalance: coin}
						state := &fakeStateClient{balanceResponse: &rpcv2.GetBalanceResponse{Balance: balance}, listBalancesResponses: []*rpcv2.ListBalancesResponse{{Balances: []*rpcv2.Balance{balance}}}}
						backend := &SuiBackend{state: state}
						request := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":["0xabc"]}`, method)
						recorder := httptest.NewRecorder()
						newTestHandler(backend).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/sui", strings.NewReader(request)))
						var response struct {
							Result json.RawMessage `json:"result"`
							Error  *RPCError       `json:"error"`
						}
						if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
							t.Fatal(err)
						}
						// Explicit expected valid triples; all others must fail closed.
						valid := (ti == 1 && (ai == 0 || ai == 1) && (ci == 0 || ci == 1)) ||
							(ti == 2 && ai == 0 && ci == 2) || (ti == 2 && ai == 1 && ci == 2) || (ti == 2 && ai == 1 && ci == 0) ||
							(ti == 2 && ai == 2 && (ci == 0 || ci == 1))
						if !valid {
							if response.Error == nil || response.Error.Code != -32001 || len(response.Result) != 0 {
								t.Fatalf("unsafe response: %s", recorder.Body.String())
							}
							return
						}
						if response.Error != nil {
							t.Fatal(recorder.Body.String())
						}
						var item map[string]any
						switch method {
						case "suix_getBalance":
							if err := json.Unmarshal(response.Result, &item); err != nil {
								t.Fatal(err)
							}
						case "suix_getAllBalances":
							var items []map[string]any
							if err := json.Unmarshal(response.Result, &items); err != nil {
								t.Fatal(err)
							}
							item = items[0]
						case "suix_listBalances":
							var page struct{ Balances []map[string]any }
							if err := json.Unmarshal(response.Result, &page); err != nil {
								t.Fatal(err)
							}
							item = page.Balances[0]
						}
						wantAddress := "0"
						if ai == 2 {
							wantAddress = "1"
						}
						if item["coinType"] != defaultCoinType || item["totalBalance"] != fmt.Sprint(*total) || item["fundsInAddressBalance"] != wantAddress {
							t.Fatal(item)
						}
					})
				}
			}
		}
	}
}

func TestZeroBalanceMissingIdentity(t *testing.T) {
	for _, balance := range []*rpcv2.Balance{nil, {Balance: ptr(uint64(0))}, {CoinType: ptr(" "), Balance: ptr(uint64(0))}, {CoinType: ptr(defaultCoinType), AddressBalance: ptr(uint64(0)), CoinBalance: ptr(uint64(0))}} {
		if got, err := legacyBalance(balance); err == nil || got != nil {
			t.Fatalf("accepted incomplete balance: %v %v", got, err)
		}
	}
}
