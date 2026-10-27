package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"google.golang.org/grpc"
	"reflect"
	rpcv2 "sui-adapter/internal/suiv2"
	"testing"
)

type extensionState struct {
	rpcv2.StateServiceClient
	request  *rpcv2.ListBalancesRequest
	response *rpcv2.ListBalancesResponse
	err      error
	calls    int
}

func (s *extensionState) ListBalances(_ context.Context, r *rpcv2.ListBalancesRequest, _ ...grpc.CallOption) (*rpcv2.ListBalancesResponse, error) {
	s.request = r
	s.calls++
	return s.response, s.err
}

type extensionLedger struct {
	rpcv2.LedgerServiceClient
	response *rpcv2.GetServiceInfoResponse
	err      error
	calls    int
}

func (l *extensionLedger) GetServiceInfo(context.Context, *rpcv2.GetServiceInfoRequest, ...grpc.CallOption) (*rpcv2.GetServiceInfoResponse, error) {
	l.calls++
	return l.response, l.err
}

func TestBalancePresenceAndPrecision(t *testing.T) {
	for _, mode := range []string{"valid", "zero", "nil", "type", "total", "address", "address exceeds total", "sum mismatch", "overflow"} {
		t.Run(mode, func(t *testing.T) {
			v := &rpcv2.Balance{CoinType: ptr("0x2::sui::SUI"), Balance: ptr(^uint64(0)), AddressBalance: ptr(uint64(5)), CoinBalance: ptr(^uint64(0) - 5)}
			switch mode {
			case "zero":
				v.Balance = ptr(uint64(0))
				v.AddressBalance = ptr(uint64(0))
				v.CoinBalance = ptr(uint64(0))
			case "nil":
				v = nil
			case "type":
				v.CoinType = nil
			case "total":
				v.Balance = nil
			case "address":
				v.AddressBalance = nil
			case "address exceeds total":
				v.Balance = ptr(uint64(4))
			case "sum mismatch":
				v.CoinBalance = ptr(uint64(0))
			case "overflow":
				v.CoinBalance = ptr(^uint64(0))
			}
			got, err := legacyBalance(v)
			if mode != "valid" && mode != "zero" {
				if err == nil || got != nil {
					t.Fatalf("accepted incomplete/inconsistent balance: %v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantTotal, wantAddress := "18446744073709551615", "5"
			if mode == "zero" {
				wantTotal, wantAddress = "0", "0"
			}
			if got["totalBalance"] != wantTotal || got["fundsInAddressBalance"] != wantAddress {
				t.Fatal(got)
			}
		})
	}
}
func TestBalanceDefaultCoinType(t *testing.T) {
	for _, params := range []string{`["0xabc"]`, `["0xabc",null]`} {
		s := &fakeStateClient{balanceResponse: &rpcv2.GetBalanceResponse{Balance: &rpcv2.Balance{CoinType: ptr(defaultCoinType), Balance: ptr(uint64(0)), AddressBalance: ptr(uint64(0))}}}
		_, err := (&SuiBackend{state: s}).Call(context.Background(), "suix_getBalance", json.RawMessage(params))
		if err != nil || s.balanceRequest.GetCoinType() != defaultCoinType {
			t.Fatalf("default changed: %v", err)
		}
	}
}
func TestListBalancesExtension(t *testing.T) {
	for _, params := range []string{`["0xabc"]`, `["0xabc",null,null]`, `["0xabc",1000,"AP8="]`} {
		s := &extensionState{response: &rpcv2.ListBalancesResponse{NextPageToken: []byte{1, 255}}}
		got, err := (&SuiBackend{state: s}).Call(context.Background(), "suix_listBalances", json.RawMessage(params))
		if err != nil {
			t.Fatal(err)
		}
		wantSize := uint32(50)
		if params == `["0xabc",1000,"AP8="]` {
			wantSize = 1000
			if !reflect.DeepEqual(s.request.PageToken, []byte{0, 255}) {
				t.Fatal(s.request)
			}
		}
		if s.calls != 1 || s.request.GetPageSize() != wantSize || s.request.GetOwner() != "0xabc" {
			t.Fatal(s.request)
		}
		body, _ := json.Marshal(got)
		if string(body) != `{"balances":[],"nextPageToken":"Af8="}` {
			t.Fatal(string(body))
		}
	}
	for _, params := range []string{`[]`, `[""]`, `["a",0]`, `["a",-1]`, `["a",1001]`, `["a",1.5]`, `["a","50"]`, `["a",50,"!"]`, `["a",50,123]`, `["a",50,null,4]`} {
		s := &extensionState{}
		_, err := (&SuiBackend{state: s}).Call(context.Background(), "suix_listBalances", json.RawMessage(params))
		var rpcErr *RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != invalidParams || s.calls != 0 {
			t.Fatalf("params %s: %v", params, err)
		}
	}
	for _, mode := range []string{"empty", "nil", "upstream", "invalid item", "valid item"} {
		t.Run(mode, func(t *testing.T) {
			s := &extensionState{response: &rpcv2.ListBalancesResponse{}}
			switch mode {
			case "nil":
				s.response = nil
			case "upstream":
				s.err = errors.New("unavailable")
			case "invalid item":
				s.response.Balances = []*rpcv2.Balance{{}}
			case "valid item":
				s.response.Balances = []*rpcv2.Balance{{CoinType: ptr("coin"), Balance: ptr(uint64(10)), AddressBalance: ptr(uint64(3))}}
			}
			got, err := (&SuiBackend{state: s}).listBalances(context.Background(), json.RawMessage(`["a"]`))
			if mode != "empty" && mode != "valid item" {
				if err == nil || got != nil {
					t.Fatal("invalid response accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			m := got.(map[string]any)
			if m["nextPageToken"] != nil {
				t.Fatal(m)
			}
			if mode == "valid item" && m["balances"].([]map[string]any)[0]["fundsInAddressBalance"] != "3" {
				t.Fatal(m)
			}
		})
	}
}
func TestServiceInfoExtension(t *testing.T) {
	for _, mode := range []string{"valid", "epoch zero", "nil", "chain", "epoch", "upstream", "params"} {
		t.Run(mode, func(t *testing.T) {
			const chain = "69WiPg3DAQiwdxfncX6wYQ2siKwAe6L9BZthQea3JNMD"
			l := &extensionLedger{response: &rpcv2.GetServiceInfoResponse{ChainId: ptr(chain), Epoch: ptr(^uint64(0))}}
			params := json.RawMessage(`[]`)
			switch mode {
			case "epoch zero":
				l.response.Epoch = ptr(uint64(0))
			case "nil":
				l.response = nil
			case "chain":
				l.response.ChainId = nil
			case "epoch":
				l.response.Epoch = nil
			case "upstream":
				l.err = errors.New("unavailable")
			case "params":
				params = json.RawMessage(`[1]`)
			}
			b := &SuiBackend{ledger: l}
			got, err := b.Call(context.Background(), "sui_getServiceInfo", params)
			if mode != "valid" && mode != "epoch zero" {
				if err == nil || got != nil {
					t.Fatal("invalid response accepted")
				}
				return
			}
			want := "18446744073709551615"
			if mode == "epoch zero" {
				want = "0"
			}
			if err != nil || !reflect.DeepEqual(got, map[string]any{"chainId": chain, "epoch": want}) {
				t.Fatalf("%v %v", got, err)
			}
			l.response.Epoch = ptr(uint64(123))
			got, err = b.Call(context.Background(), "sui_getServiceInfo", params)
			if err != nil || got.(map[string]any)["epoch"] != "123" || l.calls != 2 {
				t.Fatal("stale epoch")
			}
		})
	}
}

func TestBalanceMethodsRejectMissingAmounts(t *testing.T) {
	for _, method := range []string{"suix_getBalance", "suix_getAllBalances"} {
		t.Run(method, func(t *testing.T) {
			state := &fakeStateClient{
				balanceResponse: &rpcv2.GetBalanceResponse{},
				listBalancesResponses: []*rpcv2.ListBalancesResponse{{Balances: []*rpcv2.Balance{
					{CoinType: ptr("valid"), Balance: ptr(uint64(1)), AddressBalance: ptr(uint64(0))},
					{CoinType: ptr("missing")},
				}}},
			}
			got, err := (&SuiBackend{state: state}).Call(context.Background(), method, json.RawMessage(`["a"]`))
			if err == nil || got != nil {
				t.Fatalf("returned fabricated or partial balance: %v, %v", got, err)
			}
		})
	}
}
