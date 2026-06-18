package operators

// DecodeAttention models one-token autoregressive attention over an existing
// KV cache. Query rows are usually batch-size, while kvRows is context length
// times batch-size.
func (o *Operator) DecodeAttention(
	name string,
	query Tensor,
	queryRows, kvRows, hidden, numHeads int,
) Tensor {
	o.Log("%s decode attention query_rows=%d kv_rows=%d hidden=%d heads=%d",
		name, queryRows, kvRows, hidden, numHeads)

	q := o.Linear(name+" q", query, queryRows, hidden, hidden)
	kCache := o.Input(name+" k-cache", []int{kvRows, hidden})
	vCache := o.Input(name+" v-cache", []int{kvRows, hidden})

	o.Log("%s score gemm [%d,%d] x [%d,%d]^T",
		name, queryRows, hidden, kvRows, hidden)
	scoreBias := o.to.Zeros([]int{queryRows, kvRows})
	scores := o.to.Gemm(false, true, 1, 0, q, kCache, scoreBias)
	probs := o.RowSoftmax(scores, queryRows, kvRows)

	o.Log("%s value gemm [%d,%d] x [%d,%d]",
		name, queryRows, kvRows, kvRows, hidden)
	valueBias := o.to.Zeros([]int{queryRows, hidden})
	ctx := o.to.Gemm(false, false, 1, 0, probs, vCache, valueBias)
	out := o.Linear(name+" output", ctx, queryRows, hidden, hidden)

	o.Free(q)
	o.Free(kCache)
	o.Free(vCache)
	o.Free(scoreBias)
	o.Free(scores)
	o.Free(probs)
	o.Free(valueBias)
	o.Free(ctx)

	return out
}
