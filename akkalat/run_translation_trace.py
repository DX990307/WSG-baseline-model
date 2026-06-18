import argparse
from datetime import datetime
import os
from pathlib import Path
import shlex
import subprocess
import time

ROOT_DIR = Path(__file__).resolve().parent
TARGET = "400latency"

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

BENCHMARK_ALIASES = {
    "traditional": TRADITIONAL_BENCHMARKS,
    "pr-spmv": ["pagerank", "spmv"],
    "graph": ["pagerank", "spmv"],
    "experimental": EXPERIMENTAL_BENCHMARKS,
    "all": TRADITIONAL_BENCHMARKS + EXPERIMENTAL_BENCHMARKS,
}

DEFAULT_BENCHMARKS = ["traditional"]
DEFAULT_CONFIGS = ["baseline"]
DEFAULT_MAX_WG = 153600
DEFAULT_MAX_WORKERS = 1
DEFAULT_WINDOW_CYCLES = 500
DEFAULT_PHOTON_SAMPLED_WARMUP = 1024
DEFAULT_PHOTON_SAMPLED_GRANULARITY = 1024

BASE_COMMON_FLAGS = [
    "-timing",
    "-num-memory-banks=16",
    "-bandwidth=48",
    "-switch-latency=32",
    "-magic-memory-copy",
    "-report-all",
]

VPN_MSHR_BASELINE_FLAGS = [
    "-gmmu-vpn-mshr-baseline",
    "-mmutlb-vpn-mshr-baseline",
    "-gmmu-initial-ptcl-mode=false",
    "-gmmu-ptcl-threshold-low=0",
    "-gmmu-ptcl-threshold-high=1000000",
    "-mmutlb-demand-pte-only",
]

IOMMU_TLB_OPT_FLAGS = [
    "-mmutlb-flex-tlb",
]

GMMU_PREFETCH_FLAGS = [
    "-gmmu-prefetch",
]

GLOBAL_PHOTON_FLAGS = [
    "-sampled",
    "-branch-sampled",
    "-kernel-sampled",
    "-loop-sampled",
]

DEFAULT_ADAPTIVE_LOW = 4
DEFAULT_ADAPTIVE_HIGH = 16
DEFAULT_MMUTLB_PTCL_RETURN_LATENCY = 80
DEFAULT_GMMU_PTE_LOOKUP_LATENCY = 32
DEFAULT_GMMU_FLEX_PROMOTION_THRESHOLD = 3


def parse_args():
    parser = argparse.ArgumentParser(
        description=(
            "Run translation pressure and latency-breakdown experiments. "
            "Defaults to traditional benchmarks, baseline vs PASTA, with "
            "30000 workgroups."
        )
    )
    parser.add_argument(
        "--benchmarks",
        default=",".join(DEFAULT_BENCHMARKS),
        help=(
            "Comma-separated benchmark list or presets. Presets: "
            + ",".join(sorted(BENCHMARK_ALIASES))
            + "."
        ),
    )
    parser.add_argument(
        "--configs",
        default=",".join(DEFAULT_CONFIGS),
        help=(
            "Comma-separated config list. Supported: baseline,pasta,camsat,"
            "photon,baseline_photon,pasta_photon,camsat_photon."
        ),
    )
    parser.add_argument(
        "--max-wg",
        type=int,
        default=DEFAULT_MAX_WG,
        help="Pass -max-wg to each benchmark. Use 0 to disable.",
    )
    parser.add_argument(
        "--max-workers",
        type=int,
        default=DEFAULT_MAX_WORKERS,
        help="Maximum number of benchmark processes to run concurrently.",
    )
    parser.add_argument(
        "--window-cycles",
        type=int,
        default=DEFAULT_WINDOW_CYCLES,
        help="Cycle window for *_translation_pressure.csv.",
    )
    parser.add_argument(
        "--timeout-minutes",
        type=float,
        default=0.0,
        help="Kill each experiment after this many minutes. 0 disables timeout.",
    )
    parser.add_argument(
        "--extra-benchmark-flags",
        default="",
        help="Additional flags appended to every benchmark command.",
    )
    parser.add_argument(
        "--photon",
        action="store_true",
        help="Append Photon sampled flags to every selected config.",
    )
    parser.add_argument(
        "--sampled-warmup",
        type=int,
        default=DEFAULT_PHOTON_SAMPLED_WARMUP,
        help="Photon -sampled-warmup value.",
    )
    parser.add_argument(
        "--sampled-granularity",
        type=int,
        default=DEFAULT_PHOTON_SAMPLED_GRANULARITY,
        help="Photon -sampled-granularity value.",
    )
    parser.add_argument(
        "--output-dir",
        default="",
        help="Use this result directory instead of creating a timestamped one.",
    )
    parser.add_argument(
        "--skip-build",
        action="store_true",
        help="Do not rebuild the 400latency binary before running.",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Print commands without building or running.",
    )
    parser.add_argument(
        "--adaptive-threshold-low",
        type=int,
        default=DEFAULT_ADAPTIVE_LOW,
    )
    parser.add_argument(
        "--adaptive-threshold-high",
        type=int,
        default=DEFAULT_ADAPTIVE_HIGH,
    )
    parser.add_argument(
        "--mmutlb-ptcl-return-latency",
        type=int,
        default=DEFAULT_MMUTLB_PTCL_RETURN_LATENCY,
    )
    parser.add_argument(
        "--gmmu-pte-lookup-latency",
        type=int,
        default=DEFAULT_GMMU_PTE_LOOKUP_LATENCY,
    )
    parser.add_argument(
        "--gmmu-flex-promotion-threshold",
        type=int,
        default=DEFAULT_GMMU_FLEX_PROMOTION_THRESHOLD,
    )
    return parser.parse_args()


def parse_csv(value):
    return [item.strip() for item in value.split(",") if item.strip()]


def unique_preserving_order(items):
    seen = set()
    unique = []
    for item in items:
        if item in seen:
            continue
        seen.add(item)
        unique.append(item)
    return unique


def expand_benchmarks(raw_value):
    expanded = []
    for item in parse_csv(raw_value):
        if item in BENCHMARK_ALIASES:
            expanded.extend(BENCHMARK_ALIASES[item])
        else:
            expanded.append(item)
    return unique_preserving_order(expanded)


def adaptive_flags(low, high):
    if low > high:
        low, high = high, low
    return [
        "-gmmu-initial-ptcl-mode=false",
        f"-gmmu-ptcl-threshold-low={low}",
        f"-gmmu-ptcl-threshold-high={high}",
    ]


def config_flags(args):
    photon = photon_flags(args)
    flex_flags = [
        "-gmmu-flex-tlb",
        f"-gmmu-flex-promotion-threshold={args.gmmu_flex_promotion_threshold}",
    ]
    adaptive = adaptive_flags(
        args.adaptive_threshold_low,
        args.adaptive_threshold_high,
    )
    baseline = VPN_MSHR_BASELINE_FLAGS
    pasta = adaptive + flex_flags + IOMMU_TLB_OPT_FLAGS + GMMU_PREFETCH_FLAGS
    camsat = adaptive + IOMMU_TLB_OPT_FLAGS + ["-mmu-walk-coalescing"]
    return {
        "baseline": baseline,
        "pasta": pasta,
        "camsat": camsat,
        "photon": photon,
        "baseline_photon": baseline + photon,
        "pasta_photon": pasta + photon,
        "camsat_photon": camsat + photon,
    }


def photon_flags(args):
    return GLOBAL_PHOTON_FLAGS + [
        f"-sampled-warmup={args.sampled_warmup}",
        f"-sampled-granularity={args.sampled_granularity}",
    ]


def common_flags(args):
    return BASE_COMMON_FLAGS + [
        f"-mmutlb-ptcl-return-latency={args.mmutlb_ptcl_return_latency}",
        f"-gmmu-pte-lookup-latency={args.gmmu_pte_lookup_latency}",
        "-translation-trace",
        f"-translation-trace-window-cycles={args.window_cycles}",
    ]


def make_output_dir(args, create=True):
    if args.output_dir:
        out = Path(args.output_dir).resolve()
    else:
        out = ROOT_DIR / "results" / (
            datetime.now().strftime("%Y-%m-%d-%H-%M-%S-translation-trace")
        )
    if create:
        out.mkdir(parents=True, exist_ok=True)
    return out


def build_target():
    env = os.environ.copy()
    env.setdefault("GOCACHE", "/tmp/gocache")
    target_dir = ROOT_DIR / TARGET
    print(f"Building {TARGET} in {target_dir}", flush=True)
    subprocess.run(
        ["go", "build", "-buildvcs=false"],
        cwd=target_dir,
        env=env,
        check=True,
    )


def make_experiments(args, output_dir):
    available_configs = config_flags(args)
    selected_configs = expand_configs(parse_csv(args.configs), args.photon)
    unknown = sorted(set(selected_configs) - set(available_configs))
    if unknown:
        raise ValueError(
            "unknown configs: "
            + ",".join(unknown)
            + ". Supported: "
            + ",".join(sorted(available_configs))
        )

    extra_flags = shlex.split(args.extra_benchmark_flags)
    benchmarks = expand_benchmarks(args.benchmarks)
    exps = []
    for benchmark in benchmarks:
        for config in selected_configs:
            stem = output_dir / f"{TARGET}_{benchmark}_{config}"
            flags = common_flags(args)
            if args.max_wg > 0:
                flags.append(f"-max-wg={args.max_wg}")
            flags.extend(extra_flags)
            flags.extend(available_configs[config])
            flags.extend([
                f"-translation-trace-file={stem}",
                f"-metric-file-name={stem}_metrics",
            ])
            exps.append({
                "benchmark": benchmark,
                "config": config,
                "stem": stem,
                "flags": flags,
            })
    return exps


def expand_configs(configs, append_photon):
    if not append_photon:
        return configs

    expanded = []
    for config in configs:
        if config.endswith("_photon") or config == "photon":
            expanded.append(config)
        else:
            expanded.append(f"{config}_photon")
    return unique_preserving_order(expanded)


def experiment_command(exp):
    binary = ROOT_DIR / TARGET / TARGET
    return [
        str(binary),
        f'-benchmark={exp["benchmark"]}',
        *exp["flags"],
    ]


def run_experiment(exp, timeout_seconds):
    cmd = experiment_command(exp)
    cmd_str = shlex.join(cmd)
    print(cmd_str, flush=True)

    out_path = Path(f'{exp["stem"]}_out.stdout')
    start = datetime.now()
    start_monotonic = time.monotonic()
    with out_path.open("w", encoding="utf-8") as out_file:
        out_file.write(f"Executing {cmd_str}\n")
        out_file.write(f"Start time: {start}\n")
        out_file.flush()
        try:
            process = subprocess.run(
                cmd,
                cwd=ROOT_DIR,
                stdout=out_file,
                stderr=subprocess.STDOUT,
                timeout=timeout_seconds if timeout_seconds > 0 else None,
            )
            returncode = process.returncode
            timed_out = False
        except subprocess.TimeoutExpired:
            returncode = -9
            timed_out = True

        end = datetime.now()
        out_file.write(f"Return code: {returncode}\n")
        if timed_out:
            out_file.write(f"Timed out after {timeout_seconds} seconds\n")
        out_file.write(f"End time: {end}\n")
        out_file.write(f"Elapsed wall seconds: {time.monotonic() - start_monotonic:.2f}\n")

    if returncode != 0:
        raise RuntimeError(f"experiment failed: {cmd_str}")

    expected = [
        Path(f'{exp["stem"]}_metrics.csv'),
        Path(f'{exp["stem"]}_translation_pressure.csv'),
        Path(f'{exp["stem"]}_translation_breakdown.csv'),
    ]
    missing = [str(path) for path in expected if not path.exists()]
    if missing:
        raise RuntimeError(f"missing output files for {cmd_str}: {missing}")


def launch_experiment(exp):
    cmd = experiment_command(exp)
    cmd_str = shlex.join(cmd)
    print(cmd_str, flush=True)

    out_path = Path(f'{exp["stem"]}_out.stdout')
    out_file = out_path.open("w", encoding="utf-8")
    start = datetime.now()
    out_file.write(f"Executing {cmd_str}\n")
    out_file.write(f"Start time: {start}\n")
    out_file.flush()

    process = subprocess.Popen(
        cmd,
        cwd=ROOT_DIR,
        stdout=out_file,
        stderr=subprocess.STDOUT,
    )

    return {
        "exp": exp,
        "process": process,
        "cmd_str": cmd_str,
        "out_file": out_file,
        "start": start,
        "start_monotonic": time.monotonic(),
    }


def expected_outputs(exp):
    return [
        Path(f'{exp["stem"]}_metrics.csv'),
        Path(f'{exp["stem"]}_translation_pressure.csv'),
        Path(f'{exp["stem"]}_translation_breakdown.csv'),
    ]


def finalize_experiment(state, timeout_seconds):
    process = state["process"]
    timed_out = False
    if timeout_seconds > 0 and process.poll() is None:
        elapsed = time.monotonic() - state["start_monotonic"]
        if elapsed >= timeout_seconds:
            process.kill()
            process.wait()
            timed_out = True

    if process.poll() is None:
        return None

    end = datetime.now()
    out_file = state["out_file"]
    out_file.write(f"Return code: {process.returncode}\n")
    if timed_out:
        out_file.write(f"Timed out after {timeout_seconds} seconds\n")
    out_file.write(f"End time: {end}\n")
    out_file.write(
        f"Elapsed wall seconds: {time.monotonic() - state['start_monotonic']:.2f}\n"
    )
    out_file.close()

    exp = state["exp"]
    if process.returncode != 0:
        return {
            "exp": exp,
            "returncode": process.returncode,
            "error": "timeout" if timed_out else "failed",
            "cmd": state["cmd_str"],
        }

    missing = [str(path) for path in expected_outputs(exp) if not path.exists()]
    if missing:
        return {
            "exp": exp,
            "returncode": -1,
            "error": "missing outputs",
            "missing": missing,
            "cmd": state["cmd_str"],
        }

    return {"exp": exp, "returncode": 0}


def run_experiments(exps, max_workers, timeout_seconds):
    if max_workers <= 0:
        raise ValueError("--max-workers must be greater than 0")

    queued = list(exps)
    running = []
    completed = 0
    failed = 0

    while queued or running:
        while queued and len(running) < max_workers:
            running.append(launch_experiment(queued.pop(0)))

        still_running = []
        for state in running:
            result = finalize_experiment(state, timeout_seconds)
            if result is None:
                still_running.append(state)
                continue

            completed += 1
            if result["returncode"] != 0:
                failed += 1
                print(result, flush=True)
            else:
                exp = result["exp"]
                print(
                    f'Completed {exp["benchmark"]}/{exp["config"]} '
                    f"({completed}/{len(exps)})",
                    flush=True,
                )

        running = still_running
        if queued or running:
            time.sleep(1.0)

    print(f"Completed={completed} failed={failed}", flush=True)
    if failed > 0:
        raise SystemExit(1)


def main():
    args = parse_args()
    output_dir = make_output_dir(args, create=not args.dry_run)
    exps = make_experiments(args, output_dir)
    timeout_seconds = int(args.timeout_minutes * 60)

    print(f"Output directory: {output_dir}")
    print(f"Queued {len(exps)} experiments")
    print(f"Max workers: {args.max_workers}")

    if args.dry_run:
        for exp in exps:
            print(shlex.join(experiment_command(exp)))
        return

    if not args.skip_build:
        build_target()

    run_experiments(exps, args.max_workers, timeout_seconds)

    print(f"Done. Results are in {output_dir}")


if __name__ == "__main__":
    main()
