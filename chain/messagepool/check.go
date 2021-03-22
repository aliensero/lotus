package messagepool

import (
	"context"
	"fmt"
	stdbig "math/big"
	"sort"

	"golang.org/x/xerrors"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/lotus/api"
	"github.com/filecoin-project/lotus/build"
	"github.com/filecoin-project/lotus/chain/types"
	"github.com/filecoin-project/lotus/chain/vm"
)

var baseFeeUpperBoundFactor = types.NewInt(10)

// CheckMessages performs a set of logic checks for a list of messages, prior to submitting it to the mpool
func (mp *MessagePool) CheckMessages(msgs []*types.Message) (result []api.MessageCheckStatus, err error) {
	return mp.checkMessages(msgs, false)
}

// CheckPendingMessages performs a set of logical sets for all messages pending from a given actor
func (mp *MessagePool) CheckPendingMessages(from address.Address) (result []api.MessageCheckStatus, err error) {
	var msgs []*types.Message
	mp.lk.Lock()
	mset, ok := mp.pending[from]
	if ok {
		for _, sm := range mset.msgs {
			msgs = append(msgs, &sm.Message)
		}
	}
	mp.lk.Unlock()

	if len(msgs) == 0 {
		return nil, nil
	}

	sort.Slice(msgs, func(i, j int) bool {
		return msgs[i].Nonce < msgs[j].Nonce
	})

	return mp.checkMessages(msgs, true)
}

func (mp *MessagePool) checkMessages(msgs []*types.Message, interned bool) (result []api.MessageCheckStatus, err error) {
	mp.curTsLk.Lock()
	curTs := mp.curTs
	mp.curTsLk.Unlock()

	epoch := curTs.Height()

	var baseFee big.Int
	if len(curTs.Blocks()) > 0 {
		baseFee = curTs.Blocks()[0].ParentBaseFee
	} else {
		baseFee, err = mp.api.ChainComputeBaseFee(context.Background(), curTs)
		if err != nil {
			return nil, xerrors.Errorf("error computing basefee: %w", err)
		}
	}

	baseFeeLowerBound := getBaseFeeLowerBound(baseFee, baseFeeLowerBoundFactor)
	baseFeeUpperBound := types.BigMul(baseFee, baseFeeUpperBoundFactor)

	type actorState struct {
		nextNonce     uint64
		requiredFunds *stdbig.Int
	}

	state := make(map[address.Address]*actorState)
	balances := make(map[address.Address]big.Int)

	for _, m := range msgs {
		// basic syntactic checks
		// 1. Serialization
		check := api.MessageCheckStatus{
			Cid: m.Cid(),
			CheckStatus: api.CheckStatus{
				Code: api.CheckStatusMessageSerialize,
			},
		}

		bytes, err := m.Serialize()
		if err != nil {
			check.OK = false
			check.Err = err.Error()
		} else {
			check.OK = true
		}

		result = append(result, check)

		// 2. Message size
		check = api.MessageCheckStatus{
			Cid: m.Cid(),
			CheckStatus: api.CheckStatus{
				Code: api.CheckStatusMessageSize,
			},
		}

		if len(bytes) > 32*1024-128 { // 128 bytes to account for signature size
			check.OK = false
			check.Err = "message too big"
		} else {
			check.OK = true
		}

		result = append(result, check)

		// 3. Syntactic validation
		check = api.MessageCheckStatus{
			Cid: m.Cid(),
			CheckStatus: api.CheckStatus{
				Code: api.CheckStatusMessageValidity,
			},
		}

		if err := m.ValidForBlockInclusion(0, build.NewestNetworkVersion); err != nil {
			check.OK = false
			check.Err = fmt.Sprintf("syntactically invalid message: %s", err.Error())
		} else {
			check.OK = true
		}

		result = append(result, check)
		if !check.OK {
			// skip remaining checks if it is a syntatically invalid message
			continue
		}

		// gas checks

		// 4. Min Gas
		minGas := vm.PricelistByEpoch(epoch).OnChainMessage(m.ChainLength())

		check = api.MessageCheckStatus{
			Cid: m.Cid(),
			CheckStatus: api.CheckStatus{
				Code: api.CheckStatusMessageMinGas,
				Hint: map[string]interface{}{
					"minGas": minGas,
				},
			},
		}

		if m.GasLimit < minGas.Total() {
			check.OK = false
			check.Err = "GasLimit less than epoch minimum gas"
		} else {
			check.OK = true
		}

		result = append(result, check)

		// 5. Min Base Fee
		check = api.MessageCheckStatus{
			Cid: m.Cid(),
			CheckStatus: api.CheckStatus{
				Code: api.CheckStatusMessageMinBaseFee,
			},
		}

		if m.GasFeeCap.LessThan(minimumBaseFee) {
			check.OK = false
			check.Err = "GasFeeCap less than minimum base fee"
		} else {
			check.OK = true
		}

		result = append(result, check)
		if !check.OK {
			goto checkState
		}

		// 6. Base Fee
		check = api.MessageCheckStatus{
			Cid: m.Cid(),
			CheckStatus: api.CheckStatus{
				Code: api.CheckStatusMessageBaseFee,
				Hint: map[string]interface{}{
					"baseFee": baseFee,
				},
			},
		}

		if m.GasFeeCap.LessThan(baseFee) {
			check.OK = false
			check.Err = "GasFeeCap less than current base fee"
		} else {
			check.OK = true
		}

		result = append(result, check)

		// 7. Base Fee lower bound
		check = api.MessageCheckStatus{
			Cid: m.Cid(),
			CheckStatus: api.CheckStatus{
				Code: api.CheckStatusMessageBaseFeeLowerBound,
				Hint: map[string]interface{}{
					"baseFeeLowerBound": baseFeeLowerBound,
				},
			},
		}

		if m.GasFeeCap.LessThan(baseFeeLowerBound) {
			check.OK = false
			check.Err = "GasFeeCap less than base fee lower bound for inclusion in next 20 epochs"
		} else {
			check.OK = true
		}

		result = append(result, check)
		if !check.OK {
			goto checkState
		}

		// 8. Base Fee upper bound
		check = api.MessageCheckStatus{
			Cid: m.Cid(),
			CheckStatus: api.CheckStatus{
				Code: api.CheckStatusMessageBaseFeeUpperBound,
				Hint: map[string]interface{}{
					"baseFeeUpperBound": baseFeeUpperBound,
				},
			},
		}

		if m.GasFeeCap.LessThan(baseFeeUpperBound) {
			check.OK = false
			check.Err = "GasFeeCap less than base fee upper bound for inclusion in next 20 epochs"
		} else {
			check.OK = true
		}

		result = append(result, check)

		// stateful checks
	checkState:
		st, ok := state[m.From]
		if !ok {
			mp.lk.Lock()
			mset, ok := mp.pending[m.From]
			if ok && !interned {
				st = &actorState{nextNonce: mset.nextNonce, requiredFunds: mset.requiredFunds}
				for _, m := range mset.msgs {
					st.requiredFunds = new(stdbig.Int).Add(st.requiredFunds, m.Message.Value.Int)
				}
				state[m.From] = st
				mp.lk.Unlock()
			} else {
				mp.lk.Unlock()

				// 9. GetStateNonce
				check = api.MessageCheckStatus{
					Cid: m.Cid(),
					CheckStatus: api.CheckStatus{
						Code: api.CheckStatusMessageGetStateNonce,
					},
				}

				stateNonce, err := mp.getStateNonce(m.From, curTs)
				if err != nil {
					check.OK = false
					check.Err = fmt.Sprintf("error retrieving state nonce: %s", err.Error())
				} else {
					check.OK = true
					check.Hint = map[string]interface{}{
						"nonce": stateNonce,
					}
				}

				result = append(result, check)
				if !check.OK {
					continue
				}

				st = &actorState{nextNonce: stateNonce, requiredFunds: new(stdbig.Int)}
				state[m.From] = st
			}
		}

		// 10. Message Nonce
		check = api.MessageCheckStatus{
			Cid: m.Cid(),
			CheckStatus: api.CheckStatus{
				Code: api.CheckStatusMessageNonce,
				Hint: map[string]interface{}{
					"nextNonce": st.nextNonce,
				},
			},
		}

		if st.nextNonce != m.Nonce {
			check.OK = false
			check.Err = fmt.Sprintf("message nonce doesn't match next nonce (%d)", st.nextNonce)
		} else {
			check.OK = true
			st.nextNonce++
		}

		result = append(result, check)

		// required funds -vs- balance
		st.requiredFunds = new(stdbig.Int).Add(st.requiredFunds, m.RequiredFunds().Int)
		st.requiredFunds.Add(st.requiredFunds, m.Value.Int)

		balance, ok := balances[m.From]
		if !ok {
			// 11. GetStateBalance
			check = api.MessageCheckStatus{
				Cid: m.Cid(),
				CheckStatus: api.CheckStatus{
					Code: api.CheckStatusMessageGetStateBalance,
				},
			}

			balance, err = mp.getStateBalance(m.From, curTs)
			if err != nil {
				check.OK = false
				check.Err = fmt.Sprintf("error retrieving state balance: %s", err)
			} else {
				check.OK = true
				check.Hint = map[string]interface{}{
					"balance": balance,
				}
			}

			result = append(result, check)
			if !check.OK {
				continue
			}

			balances[m.From] = balance
		}

		// 12. Balance
		check = api.MessageCheckStatus{
			Cid: m.Cid(),
			CheckStatus: api.CheckStatus{
				Code: api.CheckStatusMessageBalance,
				Hint: map[string]interface{}{
					"requiredFunds": big.Int{Int: stdbig.NewInt(0).Set(st.requiredFunds)},
					"balance":       balance,
				},
			},
		}

		if balance.Int.Cmp(st.requiredFunds) < 0 {
			check.OK = false
			check.Err = "insufficient balance"
		} else {
			check.OK = true
		}

		result = append(result, check)
	}

	return result, nil
}
