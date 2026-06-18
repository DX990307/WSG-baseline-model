import argparse
from datetime import datetime
import os
from pathlib import Path
import shlex
import subprocess
import time

ROOT_DIR = os.path.dirname(os.path.abspath(__file__))
TARGETS = [
    "400latency",
]

DEFAULT_MAX_WORKERS = 0
DEFAULT_MAX_WORKLOADS = 16
DEFAULT_MIN_FREE_RAM_GB = 60.0
DEFAULT_MEMORY_SCAN_INTERVAL_MINUTES = 30.0
INITIAL_FILL_SETTLE_SECONDS = 2.0

TRADITIONAL_BENCHMARKS = [
    "bitonicsort",
    "relu",
    "spmv",
    "matrixmultiplication",
    "matrixtranspose",
    "fastwalshtransform",
    "fft",
    "kmeans",
    "im2col",
    "aes",
    "floydwarshall",
    "pagerank",
    "simpleconvolution",
    "fir",
]

EXPERIMENTAL_BENCHMARKS = [
    "resnet",
    "llmop",
    "llminference",
    "matrixmultiplication-ptw",
    "matrixmultiplication-ptw-heavy",
]

REMOVED_MONOLITHIC_LLM_BENCHMARKS = {
    "bert",
    "gpt",
    "kvcache",
    "kvcache-decode",
    "kvcache-decode-30b",
}

ALL_BENCHMARKS = list(dict.fromkeys(
    TRADITIONAL_BENCHMARKS + EXPERIMENTAL_BENCHMARKS
))

DEFAULT_RUN_BENCHMARKS = [
    # Edit this list to control the default run set when --benchmarks is omitted.
    # Comment out any workload you do not want in the default sweep.
    "bitonicsort",
    "relu",
    "spmv",
    "matrixmultiplication",
    "matrixtranspose",
    "fastwalshtransform",
    "fft",
    "kmeans",
    "im2col",
    "aes",
    "floydwarshall",
    "pagerank",
    "simpleconvolution",
    "fir",
    "resnet",
    "llmop",
    "llminference",
    "matrixmultiplication-ptw",
    "matrixmultiplication-ptw-heavy",
]

BENCHMARK_ALIASES = {
    "all": ALL_BENCHMARKS,
    "traditional": TRADITIONAL_BENCHMARKS,
    "llm": ["llmop"],
    "experimental": EXPERIMENTAL_BENCHMARKS,
}


# Configure which benchmarks to run for each target here. By default, this uses
# DEFAULT_RUN_BENCHMARKS above so the default run set is controlled in-script.
BENCHMARKS_BY_TARGET = {
    "400latency": DEFAULT_RUN_BENCHMARKS,
    # "TLBSensitiveStudy": ["all"],
}

DEFAULT_BENCHMARK_FLAGS = [
    "-max-wg=157200",
    # "-max-wg=38400",
]

BASE_COMMON_FLAGS = [
    "-timing",
    "-num-memory-banks=16",
    "-bandwidth=48",
    "-switch-latency=32",
    "-magic-memory-copy",
    "-report-all",
]

DEFAULT_ADAPTIVE_LOW = 4
DEFAULT_ADAPTIVE_HIGH = 16
DEFAULT_ADAPTIVE_THRESHOLD_PAIRS = "0:2,1:4,2:6,2:8,4:12,4:16,8:24"
DEFAULT_MMUTLB_PTCL_RETURN_LATENCY = 80
DEFAULT_MMUTLB_LOOKUP_LATENCY = 80
DEFAULT_GMMU_PTE_LOOKUP_LATENCY = 32
DEFAULT_GMMU_FLEX_PROMOTION_THRESHOLD = 3
DEFAULT_TIMEOUT_MINUTES = 0.0
DEFAULT_PHOTON_SAMPLED_WARMUP = 512
DEFAULT_PHOTON_SAMPLED_GRANULARITY = 512

GLOBAL_PHOTON_FLAGS = [
    "-sampled",
    "-branch-sampled",
    "-kernel-sampled",
    "-loop-sampled",
]

COALESCING_FLAGS = [
    "-mmu-walk-coalescing",
]

GMMU_PREFETCH_FLAGS = [
    "-gmmu-prefetch",
]

IOMMU_TLB_OPT_FLAGS = [
    "-mmutlb-flex-tlb",
]

VPN_MSHR_BASELINE_FLAGS = [
    "-gmmu-vpn-mshr-baseline",
    "-mmutlb-vpn-mshr-baseline",
    "-gmmu-initial-ptcl-mode=false",
    "-gmmu-ptcl-threshold-low=0",
    "-gmmu-ptcl-threshold-high=1000000",
    "-mmutlb-demand-pte-only",
]

CONFIGS = [
    ("baseline", []),
    ("sample_all", ["-sampled", "-branch-sampled", "-kernel-sampled"]),
    (
        "sample_all_loop",
        ["-sampled", "-branch-sampled", "-kernel-sampled", "-loop-sampled"],
    ),
    ("sample_wf", ["-sampled"]),
    ("sample_branch", ["-branch-sampled"]),
    ("sample_kernel", ["-kernel-sampled"]),
    ("sample_loop", ["-loop-sampled"]),
]

PTCL_CONFIG_NAMES = [
    "baseline",
    "gmmu_prefetch",
    "flex_entry",
    "ptcl_mode",
    "ptcl_flex",
    "ptcl_mode_flex",
    "pasta",
    "coalescing",
    "camsat",
]

output_dir = ""



def parse_args():
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--only-config",
        dest="only_config",
        default="",
        help="Only run experiments with this ablation config name (e.g. 'camsat').",
    )
    parser.add_argument(
        "--rerun-missing",
        dest="rerun_missing",
        default="",
        help="Reuse an existing results directory and rerun only experiments whose metrics CSV is missing.",
    )
    parser.add_argument(
        "--max-workers",
        dest="max_workers",
        type=int,
        default=DEFAULT_MAX_WORKERS,
        help=(
            "Legacy optional additional cap on concurrent experiments. 0 means "
            "only --max-workloads and available RAM control launches."
        ),
    )
    parser.add_argument(
        "--max-workloads",
        dest="max_workloads",
        type=int,
        default=DEFAULT_MAX_WORKLOADS,
        help=(
            "Hard cap on concurrently running benchmark workloads. Must be "
            f"between 1 and {DEFAULT_MAX_WORKLOADS}."
        ),
    )
    parser.add_argument(
        "--benchmarks",
        dest="benchmarks",
        default="",
        help=(
            "Comma-separated benchmark list. Presets: "
            + ",".join(sorted(BENCHMARK_ALIASES))
            + "."
        ),
    )
    parser.add_argument(
        "--configs",
        dest="configs",
        default="",
        help=(
            "Comma-separated config list. PTCL/CamSAT choices: "
            + ",".join(PTCL_CONFIG_NAMES)
            + ". Photon sampled choices: "
            + ",".join(name for name, _ in CONFIGS)
            + ". Use ptcl_all or photon_all/all for grouped configs."
        ),
    )
    parser.add_argument(
        "--extra-benchmark-flags",
        dest="extra_benchmark_flags",
        default="",
        help="Additional flags appended to each benchmark binary command.",
    )
    parser.add_argument(
        "--max-wg",
        dest="max_wg",
        type=int,
        default=None,
        help=(
            "Pass -max-wg to each benchmark. 0 disables the default cap."
        ),
    )
    parser.add_argument(
        "--timeout-minutes",
        dest="timeout_minutes",
        type=float,
        default=DEFAULT_TIMEOUT_MINUTES,
        help="Kill an experiment after this many minutes. 0 disables timeout.",
    )
    parser.add_argument(
        "--min-free-ram-gb",
        dest="min_free_ram_gb",
        type=float,
        default=DEFAULT_MIN_FREE_RAM_GB,
        help=(
            "Minimum Linux MemAvailable, in GiB, required before launching the "
            "next benchmark."
        ),
    )
    parser.add_argument(
        "--memory-scan-interval-minutes",
        dest="memory_scan_interval_minutes",
        type=float,
        default=DEFAULT_MEMORY_SCAN_INTERVAL_MINUTES,
        help="How often to check MemAvailable and consider launching one benchmark.",
    )
    parser.add_argument(
        "--photon-debug",
        action="store_true",
        help="Add -photon-debug to sampled WSG-style configs.",
    )
    parser.add_argument(
        "--photon",
        action="store_true",
        help=(
            "Append the script-level Photon sampled flags to every selected "
            "ablation config."
        ),
    )
    parser.add_argument(
        "--photon-verbose",
        action="store_true",
        help="Add -photon-debug and -photon-debug-verbose to sampled configs.",
    )
    parser.add_argument(
        "--disable-servers",
        action="store_true",
        help="Deprecated no-op. Servers are always left enabled.",
    )
    parser.add_argument(
        "--sampled-warmups",
        default="",
        help="Comma-separated -sampled-warmup values for sampled configs.",
    )
    parser.add_argument(
        "--sampled-granularities",
        default="",
        help="Comma-separated -sampled-granularity values for sampled configs.",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Print commands without building or running them.",
    )
    parser.add_argument(
        "--adaptive-threshold-low",
        dest="adaptive_threshold_low",
        type=int,
        default=DEFAULT_ADAPTIVE_LOW,
        help="Default adaptive low threshold used in non-scan mode.",
    )
    parser.add_argument(
        "--adaptive-threshold-high",
        dest="adaptive_threshold_high",
        type=int,
        default=DEFAULT_ADAPTIVE_HIGH,
        help="Default adaptive high threshold used in non-scan mode.",
    )
    parser.add_argument(
        "--adaptive-threshold-scan",
        dest="adaptive_threshold_scan",
        action="store_true",
        help="Scan adaptive thresholds with PTCL adaptation and MMU coalescing enabled.",
    )
    parser.add_argument(
        "--ptcl-flex-test",
        dest="ptcl_flex_test",
        action="store_true",
        help="Run only ptcl_mode, ptcl_flex, and ptcl_mode_flex configs.",
    )
    parser.add_argument(
        "--adaptive-threshold-pairs",
        dest="adaptive_threshold_pairs",
        default=DEFAULT_ADAPTIVE_THRESHOLD_PAIRS,
        help='Comma-separated low:high pairs, for example "2:6,4:12,20:60".',
    )
    parser.add_argument(
        "--mmutlb-ptcl-return-latency",
        dest="mmutlb_ptcl_return_latency",
        type=int,
        default=DEFAULT_MMUTLB_PTCL_RETURN_LATENCY,
        help="Fixed MMUTLB/IOTLB lookup latency per requested PTE (per bitmap bit), in cycles, applied before each buffered translation request is looked up.",
    )
    parser.add_argument(
        "--gmmu-pte-lookup-latency",
        dest="gmmu_pte_lookup_latency",
        type=int,
        default=DEFAULT_GMMU_PTE_LOOKUP_LATENCY,
        help="Fixed GMMU L2 TLB lookup latency per internal PTE lookup job, in cycles.",
    )
    parser.add_argument(
        "--gmmu-ptcl-serial-lookup",
        dest="gmmu_ptcl_serial_lookup",
        action="store_true",
        help="Model non-flex GMMU PTCL lookup as one serial bitmap lookup instead of parallel per-bit lookup jobs.",
    )
    parser.add_argument(
        "--gmmu-flex-promotion-threshold",
        dest="gmmu_flex_promotion_threshold",
        type=int,
        default=DEFAULT_GMMU_FLEX_PROMOTION_THRESHOLD,
        help="Minimum valid bitmap fill bits before Flex stores a PTCL-line entry.",
    )
    return parser.parse_args()


def parse_csv(value):
    return [item.strip() for item in value.split(",") if item.strip()]


def parse_int_csv(value, label):
    values = []
    for item in parse_csv(value):
        parsed = int(item)
        if parsed <= 0:
            raise ValueError(f"{label} values must be positive: {item}")
        values.append(parsed)
    return values


def unique_preserving_order(items):
    seen = set()
    unique = []
    for item in items:
        if item in seen:
            continue
        seen.add(item)
        unique.append(item)
    return unique


def expand_benchmark_selection(selected):
    expanded = []
    for item in selected:
        if item in BENCHMARK_ALIASES:
            expanded += BENCHMARK_ALIASES[item]
        else:
            expanded.append(item)
    blocked = [
        item for item in expanded
        if item in REMOVED_MONOLITHIC_LLM_BENCHMARKS
    ]
    if blocked:
        raise ValueError(
            "monolithic LLM benchmarks were removed from runall2.py: "
            + ",".join(blocked)
            + ". Use runllm_decomposed.py for BERT/GPT experiments."
        )
    return unique_preserving_order(expanded)


def parse_threshold_pairs(raw_pairs):
    pairs = []
    for token in raw_pairs.split(","):
        token = token.strip()
        if not token:
            continue

        if ":" not in token:
            raise ValueError(f"invalid threshold pair '{token}', expected low:high")

        low_s, high_s = token.split(":", 1)
        low = int(low_s.strip())
        high = int(high_s.strip())
        if low > high:
            low, high = high, low
        pairs.append((low, high))

    if not pairs:
        raise ValueError("no valid adaptive threshold pairs provided")

    return pairs


def adaptive_flags(low, high):
    return [
        "-gmmu-initial-ptcl-mode=false",
        f"-gmmu-ptcl-threshold-low={low}",
        f"-gmmu-ptcl-threshold-high={high}",
    ]


def build_common_flags(args):
    flags = BASE_COMMON_FLAGS + [
        f"-mmutlb-ptcl-return-latency={args.mmutlb_ptcl_return_latency}",
        f"-gmmu-pte-lookup-latency={args.gmmu_pte_lookup_latency}",
    ]
    if args.gmmu_ptcl_serial_lookup:
        flags.append("-gmmu-ptcl-serial-lookup")
    return flags


def build_ablation_configs(args):
    if args.adaptive_threshold_scan:
        if args.configs:
            raise ValueError(
                "--adaptive-threshold-scan cannot be combined with --configs"
            )
        threshold_pairs = parse_threshold_pairs(args.adaptive_threshold_pairs)
        return [
            (
                f"adaptive_l{low}_h{high}_camsat",
                adaptive_flags(low, high) + IOMMU_TLB_OPT_FLAGS + COALESCING_FLAGS,
            )
            for low, high in threshold_pairs
        ]

    ptcl_configs = build_ptcl_config_map(args)
    if args.ptcl_flex_test:
        if args.configs:
            raise ValueError("--ptcl-flex-test cannot be combined with --configs")
        return [
            ("ptcl_mode", ptcl_configs["ptcl_mode"]),
            ("ptcl_flex", ptcl_configs["ptcl_flex"]),
            ("ptcl_mode_flex", ptcl_configs["ptcl_mode_flex"]),
        ]

    if args.configs:
        return build_selected_configs(args, ptcl_configs)

    return [(name, ptcl_configs[name]) for name in PTCL_CONFIG_NAMES]


def build_ptcl_config_map(args):
    low = args.adaptive_threshold_low
    high = args.adaptive_threshold_high
    if low > high:
        low, high = high, low
    flex_flags = [
        "-gmmu-flex-tlb",
        f"-gmmu-flex-promotion-threshold={args.gmmu_flex_promotion_threshold}",
    ]

    return {
        "baseline": VPN_MSHR_BASELINE_FLAGS,
        "gmmu_prefetch": VPN_MSHR_BASELINE_FLAGS + GMMU_PREFETCH_FLAGS,
        "flex_entry": VPN_MSHR_BASELINE_FLAGS + flex_flags,
        "ptcl_mode": adaptive_flags(low, high)
        + IOMMU_TLB_OPT_FLAGS
        + ["-gmmu-ptcl-serial-lookup"],
        "ptcl_flex": VPN_MSHR_BASELINE_FLAGS + flex_flags,
        "ptcl_mode_flex": adaptive_flags(low, high)
        + flex_flags
        + IOMMU_TLB_OPT_FLAGS,
        "pasta": adaptive_flags(low, high)
        + flex_flags
        + IOMMU_TLB_OPT_FLAGS
        + GMMU_PREFETCH_FLAGS,
        "coalescing": VPN_MSHR_BASELINE_FLAGS + COALESCING_FLAGS,
        "camsat": adaptive_flags(low, high) + IOMMU_TLB_OPT_FLAGS + COALESCING_FLAGS,
    }


def build_selected_configs(args, ptcl_configs):
    requested = parse_csv(args.configs)
    photon_configs = {name: flags for name, flags in CONFIGS}
    selected = []

    explicit_ptcl = any(
        name in ptcl_configs and name != "baseline"
        for name in requested
    )
    explicit_photon = any(
        name in photon_configs and name != "baseline"
        for name in requested
    )

    for name in requested:
        if name == "ptcl_all":
            selected += [
                (config_name, ptcl_configs[config_name])
                for config_name in PTCL_CONFIG_NAMES
            ]
            continue

        if name in ("all", "photon_all"):
            selected += build_photon_configs(args, list(photon_configs))
            continue

        if name == "baseline":
            if explicit_ptcl and not explicit_photon:
                selected.append(("baseline", ptcl_configs["baseline"]))
            else:
                selected += build_photon_configs(args, ["baseline"])
            continue

        if name in ptcl_configs:
            selected.append((name, ptcl_configs[name]))
            continue

        if name in photon_configs:
            selected += build_photon_configs(args, [name])
            continue

        allowed = sorted(
            set(PTCL_CONFIG_NAMES)
            | set(photon_configs)
            | {"ptcl_all", "photon_all", "all"}
        )
        raise ValueError(
            f"unknown config: {name}. Allowed: {', '.join(allowed)}"
        )

    return unique_preserving_config_names(selected)


def unique_preserving_config_names(configs):
    seen = set()
    unique = []
    for name, flags in configs:
        if name in seen:
            continue
        seen.add(name)
        unique.append((name, flags))
    return unique


def build_photon_configs(args, requested):
    selected = []
    for name, flags in CONFIGS:
        if name not in requested:
            continue
        config_flags = flags[:]
        if (args.photon_debug or args.photon_verbose) and name != "baseline":
            config_flags.append("-photon-debug")
        if args.photon_verbose and name != "baseline":
            config_flags.append("-photon-debug-verbose")
        selected += expand_sampled_params(args, name, config_flags)

    return selected


def expand_sampled_params(args, name, flags):
    if "-sampled" not in flags:
        return [(name, flags)]

    warmups = parse_int_csv(args.sampled_warmups, "sampled-warmups")
    granularities = parse_int_csv(
        args.sampled_granularities, "sampled-granularities")
    if not warmups and not granularities:
        return [(name, flags)]
    if not warmups:
        warmups = [1024]
    if not granularities:
        granularities = [2048]

    expanded = []
    for warmup in warmups:
        for granularity in granularities:
            expanded.append((
                f"{name}_w{warmup}_g{granularity}",
                flags + [
                    f"-sampled-warmup={warmup}",
                    f"-sampled-granularity={granularity}",
                ],
            ))
    return expanded


def get_benchmarks_for_target(target):
    selected = BENCHMARKS_BY_TARGET.get(target, ["all"])
    selected = expand_benchmark_selection(selected)

    unknown = sorted(set(selected) - set(ALL_BENCHMARKS))
    if unknown:
        raise ValueError(f"unknown benchmarks for {target}: {unknown}")

    return selected


def get_selected_benchmarks(args, target):
    if args.benchmarks:
        selected = parse_csv(args.benchmarks)
    else:
        selected = get_benchmarks_for_target(target)

    selected = expand_benchmark_selection(selected)
    unknown = sorted(set(selected) - set(ALL_BENCHMARKS))
    if unknown:
        raise ValueError(f"unknown benchmarks: {unknown}")
    return selected


def strip_disable_server_flags(flags):
    return [
        flag for flag in flags
        if flag not in ("-disable-servers", "--disable-servers")
    ]


def has_flag_with_prefix(flags, prefix):
    return any(flag.startswith(prefix) for flag in flags)


def append_unique_flag(flags, flag):
    if flag not in flags:
        flags.append(flag)


def add_global_photon_flags(args, flags):
    if not args.photon:
        return flags

    photon_flags = flags[:]
    for flag in GLOBAL_PHOTON_FLAGS:
        append_unique_flag(photon_flags, flag)

    if args.photon_debug or args.photon_verbose:
        append_unique_flag(photon_flags, "-photon-debug")
    if args.photon_verbose:
        append_unique_flag(photon_flags, "-photon-debug-verbose")

    if not has_flag_with_prefix(photon_flags, "-sampled-warmup="):
        photon_flags.append(
            f"-sampled-warmup={DEFAULT_PHOTON_SAMPLED_WARMUP}"
        )
    if not has_flag_with_prefix(photon_flags, "-sampled-granularity="):
        photon_flags.append(
            f"-sampled-granularity={DEFAULT_PHOTON_SAMPLED_GRANULARITY}"
        )

    return photon_flags


def default_benchmark_flags(args):
    if args.max_wg is not None:
        if args.max_wg <= 0:
            return []
        return [f"-max-wg={args.max_wg}"]
    return DEFAULT_BENCHMARK_FLAGS[:]


def make_exps(args, ablation_configs):
    exps = []
    extra_flags = strip_disable_server_flags(shlex.split(args.extra_benchmark_flags))
    for target in TARGETS:
        for benchmark in get_selected_benchmarks(args, target):
            for config_name, config_flags in ablation_configs:
                flags = (
                    default_benchmark_flags(args)
                    + extra_flags
                    + strip_disable_server_flags(config_flags)
                )
                flags = add_global_photon_flags(args, flags)
                exps.append(
                    {
                        "target": target,
                        "benchmark": benchmark,
                        "config_name": config_name,
                        "flags": flags,
                    }
                )
    return exps


def filter_missing_metric_exps(exps, results_dir):
    missing = []
    missing_dir = Path(results_dir)
    for exp in exps:
        stem = (
            f'{exp["target"]}_{exp["benchmark"]}_{exp["config_name"]}'
        )
        metrics_csv = missing_dir / f"{stem}_metrics.csv"
        if not metrics_csv.exists():
            missing.append(exp)

    return missing


def build_env():
    env = os.environ.copy()
    env.setdefault("GOCACHE", "/tmp/gocache")
    return env


def build_targets(exps):
    env = build_env()
    targets = sorted({exp["target"] for exp in exps})

    for target in targets:
        target_dir = os.path.join(ROOT_DIR, target)
        print(f"Building {target} in {target_dir}")
        process = subprocess.Popen(
            ["go", "build", "-buildvcs=false"], cwd=target_dir, env=env)
        process.wait()
        if process.returncode != 0:
            raise RuntimeError(f"failed to build {target}")


def exp_file_stem(exp):
    return os.path.join(
        output_dir,
        f'{exp["target"]}_{exp["benchmark"]}_{exp["config_name"]}',
    )


def experiment_command(exp):
    binary = os.path.join(ROOT_DIR, exp["target"], exp["target"])
    metric_file_name = f"{exp_file_stem(exp)}_metrics"
    return [
        binary,
        f'-benchmark={exp["benchmark"]}',
        *exp["common_flags"],
        *exp["flags"],
        f"-metric-file-name={metric_file_name}",
    ]


def read_mem_available_kb():
    try:
        with open("/proc/meminfo", "r", encoding="utf-8") as meminfo_file:
            for line in meminfo_file:
                if line.startswith("MemAvailable:"):
                    parts = line.split()
                    if len(parts) >= 2:
                        return int(parts[1])
    except (FileNotFoundError, PermissionError, ValueError):
        return None

    return None


def format_memory_kb(kb):
    if kb is None:
        return "unknown"
    if kb >= 1024 * 1024:
        return f"{kb / (1024 * 1024):.2f} GiB"
    if kb >= 1024:
        return f"{kb / 1024:.2f} MiB"
    return f"{kb} KiB"


def launch_experiment(exp):
    file_stem = exp_file_stem(exp)
    metric_file_name = f"{file_stem}_metrics"

    cmd = experiment_command(exp)
    cmd_str = shlex.join(cmd)
    print(cmd_str, flush=True)

    out_file_name = f"{file_stem}_out.stdout"
    out_file = open(out_file_name, "w", encoding="utf-8")
    start_time = datetime.now()
    launch_mem_kb = read_mem_available_kb()
    out_file.write(f"Executing {cmd_str}\n")
    out_file.write(f"Start time: {start_time}\n")
    out_file.write(
        "Launch MemAvailable: "
        f"{launch_mem_kb} KiB ({format_memory_kb(launch_mem_kb)})\n"
    )
    out_file.flush()

    process = subprocess.Popen(
        cmd,
        stdout=out_file,
        stderr=subprocess.STDOUT,
        cwd=ROOT_DIR,
        text=True,
        bufsize=1,
    )

    return {
        "exp": exp,
        "process": process,
        "cmd_str": cmd_str,
        "metric_file_name": metric_file_name,
        "out_file": out_file,
        "start_time": start_time,
        "start_monotonic": time.monotonic(),
    }


def finalize_experiment(state, timed_out=False):
    process = state["process"]
    if timed_out and process.poll() is None:
        process.kill()
        process.wait()

    end_time = datetime.now()
    elapsed_time = end_time - state["start_time"]
    out_file = state["out_file"]
    out_file.write(f"Return code: {process.returncode}\n")
    if timed_out:
        timeout_seconds = state["exp"].get("timeout_seconds", 0)
        out_file.write(f"Timed out after {timeout_seconds} seconds\n")
    out_file.write(f"End time: {end_time}\n")
    out_file.write(f"Elapsed time: {elapsed_time}\n")
    out_file.close()

    cmd_str = state["cmd_str"]
    if timed_out:
        print(f"Timed out executing {cmd_str}")
        return {
            "exp": state["exp"],
            "returncode": -9,
            "timeout": state["exp"].get("timeout_seconds", 0),
        }

    if process.returncode != 0:
        print(f"Error executing {cmd_str}")
        return {"exp": state["exp"], "returncode": process.returncode}

    metrics_csv = state["metric_file_name"] + ".csv"
    if not os.path.exists(metrics_csv):
        print(f"Missing metrics file for {cmd_str}: {metrics_csv}")
        return {
            "exp": state["exp"],
            "returncode": -1,
            "missing_metrics": metrics_csv,
        }

    print(f"Executed {cmd_str}, time {elapsed_time}")
    return {"exp": state["exp"], "returncode": 0}


def running_cap_reached(args, running):
    return len(running) >= effective_workload_cap(args)


def effective_workload_cap(args):
    cap = args.max_workloads
    if args.max_workers > 0:
        cap = min(cap, args.max_workers)
    return cap


def print_scheduler_status(prefix, queued, running, completed, failed):
    print(
        f"{prefix} queued={len(queued)} running={len(running)} "
        f"completed={completed} failed={failed}",
        flush=True,
    )


def memory_gated_run(exps, args):
    queued = list(exps)
    running = []
    completed = 0
    failed = 0
    min_mem_available_kb = int(args.min_free_ram_gb * 1024 * 1024)
    scan_interval_seconds = args.memory_scan_interval_minutes * 60
    next_scan_time = time.monotonic() + scan_interval_seconds
    initial_fill = True
    initial_fill_cap_logged = False

    print(
        "Memory gate: "
        f"MemAvailable >= {min_mem_available_kb} KiB "
        f"({format_memory_kb(min_mem_available_kb)}), "
        f"scan interval={args.memory_scan_interval_minutes} minutes",
        flush=True,
    )
    print(
        f"Max running workloads: {effective_workload_cap(args)}",
        flush=True,
    )
    if args.max_workers > 0 and args.max_workers < args.max_workloads:
        print(f"Legacy max-workers cap also applied: {args.max_workers}", flush=True)
    else:
        print("Legacy max-workers cap: disabled", flush=True)

    while queued or running:
        now = time.monotonic()
        still_running = []
        completed_this_round = False
        for state in running:
            process = state["process"]
            timeout_seconds = state["exp"].get("timeout_seconds", 0)
            timed_out = (
                timeout_seconds > 0
                and process.poll() is None
                and now - state["start_monotonic"] >= timeout_seconds
            )

            if timed_out:
                result = finalize_experiment(state, timed_out=True)
            elif process.poll() is None:
                still_running.append(state)
                continue
            else:
                result = finalize_experiment(state)

            completed_this_round = True
            completed += 1
            if result["returncode"] != 0:
                failed += 1
            print(result, flush=True)

        running = still_running
        if completed_this_round:
            initial_fill_cap_logged = False

        if queued and initial_fill:
            launched_any = False
            while queued and not running_cap_reached(args, running):
                available_kb = read_mem_available_kb()
                print(
                    "[initial-fill] "
                    f"MemAvailable={available_kb} KiB "
                    f"({format_memory_kb(available_kb)}), "
                    f"threshold={min_mem_available_kb} KiB "
                    f"({format_memory_kb(min_mem_available_kb)})",
                    flush=True,
                )
                print_scheduler_status(
                    "[initial-fill]", queued, running, completed, failed)

                if available_kb is None:
                    print(
                        "Initial fill paused: cannot read Linux MemAvailable.",
                        flush=True,
                    )
                    initial_fill = False
                    next_scan_time = time.monotonic() + scan_interval_seconds
                    break

                if available_kb < min_mem_available_kb:
                    print(
                        "Initial fill complete: not enough free RAM to launch "
                        "the next benchmark.",
                        flush=True,
                    )
                    initial_fill = False
                    next_scan_time = time.monotonic() + scan_interval_seconds
                    break

                exp = queued.pop(0)
                running.append(launch_experiment(exp))
                launched_any = True
                initial_fill_cap_logged = False
                print_scheduler_status(
                    "[launch]", queued, running, completed, failed)
                if queued and not running_cap_reached(args, running):
                    time.sleep(INITIAL_FILL_SETTLE_SECONDS)

            if queued and running_cap_reached(args, running):
                if not initial_fill_cap_logged:
                    print(
                        "Initial fill paused: optional max-workers cap is reached.",
                        flush=True,
                    )
                    print_scheduler_status(
                        "[initial-fill]", queued, running, completed, failed)
                    initial_fill_cap_logged = True

            if launched_any:
                continue

        now = time.monotonic()
        if queued and not initial_fill and now >= next_scan_time:
            available_kb = read_mem_available_kb()
            print(
                "[memory-scan] "
                f"MemAvailable={available_kb} KiB "
                f"({format_memory_kb(available_kb)}), "
                f"threshold={min_mem_available_kb} KiB "
                f"({format_memory_kb(min_mem_available_kb)})",
                flush=True,
            )
            print_scheduler_status(
                "[memory-scan]", queued, running, completed, failed)

            if available_kb is None:
                print(
                    "Waiting: cannot read Linux MemAvailable.",
                    flush=True,
                )
            elif available_kb < min_mem_available_kb:
                print(
                    "Waiting: not enough free RAM to launch the next benchmark.",
                    flush=True,
                )
            elif running_cap_reached(args, running):
                print(
                    "Waiting: optional max-workers cap is reached.",
                    flush=True,
                )
            else:
                exp = queued.pop(0)
                running.append(launch_experiment(exp))
                print_scheduler_status(
                    "[launch]", queued, running, completed, failed)

            next_scan_time = now + scan_interval_seconds

        if queued or running:
            if queued and not initial_fill:
                now = time.monotonic()
                sleep_seconds = max(0.1, min(30.0, next_scan_time - now))
            else:
                sleep_seconds = 5.0
            time.sleep(sleep_seconds)

    print_scheduler_status("[summary]", queued, running, completed, failed)
    if failed > 0:
        raise SystemExit(1)


def dry_run_commands(exps):
    for exp in exps:
        print(shlex.join(experiment_command(exp)))


def create_output_dir():
    global output_dir
    output_dir = os.path.join(
        ROOT_DIR,
        "results",
        datetime.now().strftime("%Y-%m-%d-%H-%M-%S-ptcl-prefetch-sweep"),
    )

    results_dir = os.path.join(ROOT_DIR, "results")
    if not os.path.exists(results_dir):
        os.makedirs(results_dir)

    if not os.path.exists(output_dir):
        os.makedirs(output_dir)


def main():
    global output_dir

    args = parse_args()
    common_flags = build_common_flags(args)
    ablation_configs = build_ablation_configs(args)
    if args.only_config:
        ablation_configs = [c for c in ablation_configs if c[0] == args.only_config]
        if not ablation_configs:
            raise ValueError(f"no ablation config named '{args.only_config}'")
    exps = make_exps(args, ablation_configs)
    if not exps:
        print("No experiments configured.")
        return

    if args.rerun_missing:
        output_dir = os.path.abspath(args.rerun_missing)
        if not os.path.isdir(output_dir):
            raise ValueError(f"results directory does not exist: {output_dir}")
        exps = filter_missing_metric_exps(exps, output_dir)
        if not exps:
            print(f"No missing-metrics experiments found in {output_dir}")
            return
        print(
            f"Rerunning {len(exps)} experiments with missing metrics in {output_dir}"
        )
    else:
        create_output_dir()

    timeout_seconds = int(args.timeout_minutes * 60)
    for exp in exps:
        exp["common_flags"] = common_flags
        exp["timeout_seconds"] = timeout_seconds

    if args.max_workers < 0:
        raise ValueError("--max-workers must be non-negative")
    if args.max_workloads <= 0:
        raise ValueError("--max-workloads must be greater than 0")
    if args.max_workloads > DEFAULT_MAX_WORKLOADS:
        raise ValueError(
            f"--max-workloads cannot exceed {DEFAULT_MAX_WORKLOADS}"
        )
    if args.min_free_ram_gb < 0:
        raise ValueError("--min-free-ram-gb must be non-negative")
    if args.memory_scan_interval_minutes <= 0:
        raise ValueError("--memory-scan-interval-minutes must be greater than 0")

    print(f"Using common flags: {shlex.join(common_flags)}")
    if args.photon:
        photon_defaults = GLOBAL_PHOTON_FLAGS + [
            f"-sampled-warmup={DEFAULT_PHOTON_SAMPLED_WARMUP}",
            f"-sampled-granularity={DEFAULT_PHOTON_SAMPLED_GRANULARITY}",
        ]
        print(f"Global Photon flags: {shlex.join(photon_defaults)}")
    if args.timeout_minutes > 0:
        print(f"Experiment timeout: {args.timeout_minutes} minutes")
    print(f"Queued {len(exps)} experiments")
    if args.dry_run:
        dry_run_commands(exps)
        return

    build_targets(exps)
    memory_gated_run(exps, args)


if __name__ == "__main__":
    main()
