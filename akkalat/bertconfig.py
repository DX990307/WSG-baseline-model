"""BERT decomposed llmop configuration.

This mirrors Photon's ResNet config style: build a list of small operator
benchmarks, then let the runner execute each one independently and sum results.
"""


PROFILES = {
    "tiny": {
        "batch": 1,
        "seq_len": 2,
        "hidden": 16,
        "heads": 4,
        "layers": 1,
        "intermediate": 32,
    },
    "middle": {
        "batch": 1,
        "seq_len": 64,
        "hidden": 512,
        "heads": 8,
        "layers": 2,
        "intermediate": 2048,
    },
    "bert-base": {
        "batch": 1,
        "seq_len": 128,
        "hidden": 768,
        "heads": 12,
        "layers": 12,
        "intermediate": 3072,
    },
    "bert-large": {
        "batch": 1,
        "seq_len": 128,
        "hidden": 1024,
        "heads": 16,
        "layers": 24,
        "intermediate": 4096,
    },
	"bert-7b-proxy": {
		"batch": 1,
		"seq_len": 5120,
		"hidden": 4096,
        "heads": 32,
        "layers": 32,
        "intermediate": 11008,
    },
}


def op_flags(op, **kwargs):
    flags = [f"-op={op}"]
    for key in sorted(kwargs):
        value = kwargs[key]
        if value is not None:
            flags.append(f"-{key.replace('_', '-')}={value}")
    return flags


def linear(rows, input_dim, output_dim, split_k=1):
    if split_k > 1:
        return op_flags(
            "split-linear",
            rows=rows,
            input_dim=input_dim,
            output_dim=output_dim,
            split_k=split_k,
        )
    return op_flags(
        "linear", rows=rows, input_dim=input_dim, output_dim=output_dim)


def profile(profile_name):
    if profile_name not in PROFILES:
        raise ValueError(f"unknown BERT profile {profile_name!r}")
    return PROFILES[profile_name]


def init_bert(profile_name="tiny", split_k=1, layers=None):
    profile_config = dict(profile(profile_name))
    if layers is not None:
        profile_config["layers"] = layers
    return {"llmop": bert_ops(profile_config, split_k)}


def run_bert(benchmarks):
    return benchmarks["llmop"]


def bert_ops(profile_config, split_k=1):
    batch = profile_config["batch"]
    seq_len = profile_config["seq_len"]
    hidden = profile_config["hidden"]
    layers = profile_config["layers"]
    intermediate = profile_config["intermediate"]
    rows = batch * seq_len

    ops = [("embedding", op_flags("embedding", rows=rows, hidden=hidden))]

    for layer in range(layers):
        prefix = f"layer{layer:02d}"
        ops += [
            (f"{prefix}_attn_q", linear(rows, hidden, hidden, split_k)),
            (f"{prefix}_attn_k", linear(rows, hidden, hidden, split_k)),
            (f"{prefix}_attn_v", linear(rows, hidden, hidden, split_k)),
            (f"{prefix}_attn_score", linear(rows, hidden, rows, split_k)),
            (f"{prefix}_attn_softmax",
             op_flags("row-softmax", rows=rows, cols=rows)),
            (f"{prefix}_attn_value", linear(rows, rows, hidden, split_k)),
            (f"{prefix}_attn_out", linear(rows, hidden, hidden, split_k)),
            (f"{prefix}_attn_residual",
             op_flags("residual-add", elements=rows * hidden)),
            (f"{prefix}_norm1",
             op_flags("layernorm", rows=rows, hidden=hidden)),
            (f"{prefix}_mlp_fc1",
             linear(rows, hidden, intermediate, split_k)),
            (f"{prefix}_mlp_gelu",
             op_flags("gelu", elements=rows * intermediate)),
            (f"{prefix}_mlp_fc2",
             linear(rows, intermediate, hidden, split_k)),
            (f"{prefix}_mlp_residual",
             op_flags("residual-add", elements=rows * hidden)),
            (f"{prefix}_norm2",
             op_flags("layernorm", rows=rows, hidden=hidden)),
        ]

    return ops
