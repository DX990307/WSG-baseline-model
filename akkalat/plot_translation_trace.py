#!/usr/bin/env python3
import argparse
import csv
import math
import os
import re
from pathlib import Path

os.environ.setdefault("MPLCONFIGDIR", "/tmp/matplotlib")

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt


PRESSURE_RE = re.compile(
    r"^400latency_(?P<bench>.+)_(?P<config>baseline|pasta|camsat)(?P<photon>_photon)?_translation_pressure\.csv$"
)
BREAKDOWN_RE = re.compile(
    r"^400latency_(?P<bench>.+)_(?P<config>baseline|pasta|camsat)(?P<photon>_photon)?_translation_breakdown\.csv$"
)


STAGE_ORDER = [
    ("gmmutlb_mshr_wait", "L1->L2 wait"),
    ("gmmutlb_mshr_entry_wait", "L2 MSHR entry"),
    ("gmmutlb_pte_lookup_queue", "L2 lookup queue"),
    ("gmmutlb_pte_lookup_hit_service", "L2 lookup hit"),
    ("gmmutlb_pte_lookup_miss_service", "L2 lookup miss"),
    ("iommutlb_lookup_service", "IOMMU lookup"),
    ("iommutlb_mshr_wait", "IOMMU MSHR wait"),
    ("iommutlb_mshr_entry_wait", "IOMMU MSHR entry"),
    ("iommucache_upper_level_latency", "IOMMU upper-level"),
    ("shared_mmu_pwqueue_wait", "Remote PTW queue"),
    ("shared_mmu_ptw_service", "Remote PTW service"),
    ("local_gmmu_ptw_queue_wait", "Local PTW queue"),
    ("local_gmmu_ptw_service", "Local PTW service"),
]

STAGE_COLORS = {
    "gmmutlb_mshr_wait": "#7f7f7f",
    "gmmutlb_mshr_entry_wait": "#bdbdbd",
    "gmmutlb_pte_lookup_queue": "#9ecae9",
    "gmmutlb_pte_lookup_hit_service": "#4c78a8",
    "gmmutlb_pte_lookup_miss_service": "#1f4e79",
    "iommutlb_lookup_service": "#72b7b2",
    "iommutlb_mshr_wait": "#e45756",
    "iommutlb_mshr_entry_wait": "#ff9da6",
    "iommucache_upper_level_latency": "#f58518",
    "shared_mmu_pwqueue_wait": "#b279a2",
    "shared_mmu_ptw_service": "#54a24b",
    "local_gmmu_ptw_queue_wait": "#eeca3b",
    "local_gmmu_ptw_service": "#59a14f",
}

LOOKUP_SPLIT_STAGES = (
    "gmmutlb_pte_lookup_hit_service",
    "gmmutlb_pte_lookup_miss_service",
)

PATH_ORDER = ["gmmutlb_hit", "local", "remote"]
PATH_LABELS = {
    "gmmutlb_hit": "L2 hit",
    "local": "local",
    "remote": "remote",
}

BENCH_LABELS = {
    "bitonicsort": "bitonic",
    "floydwarshall": "floyd",
    "matrixmultiplication": "mmul",
    "matrixtranspose": "mtrans",
    "fastwalshtransform": "fwt",
    "simpleconvolution": "conv",
}


def parse_args():
    parser = argparse.ArgumentParser(
        description="Plot translation pressure and request breakdown traces."
    )
    parser.add_argument(
        "result_dir",
        nargs="?",
        type=Path,
        help="Directory containing *_translation_pressure.csv and *_translation_breakdown.csv files.",
    )
    parser.add_argument(
        "--output-prefix",
        default="translation",
        help="Prefix for generated figure names.",
    )
    parser.add_argument(
        "--benchmarks",
        help="Comma-separated benchmark names to plot, for example relu,kmeans.",
    )
    parser.add_argument(
        "--split-paths",
        action="store_true",
        help="Plot one breakdown bar per path instead of aggregating paths by benchmark.",
    )
    parser.add_argument(
        "--breakdown-metric",
        choices=("percent", "per-request", "per-request-percent", "cycles"),
        default="percent",
        help="Use normalized percent, average cycles per request, per-request normalized percent, or aggregate cycles for the breakdown plot.",
    )
    parser.add_argument(
        "--full-round-trip-only",
        action="store_true",
        help="Exclude L2 TLB hit-only requests from breakdown plots and renormalize over local/remote full round-trip requests.",
    )
    return parser.parse_args()


def latest_translation_dir():
    results_dir = Path("results")
    candidates = [
        path
        for path in results_dir.glob("*-translation-trace")
        if path.is_dir()
    ]
    if not candidates:
        raise SystemExit("no *-translation-trace directory found under ./results")
    return max(candidates, key=lambda path: path.stat().st_mtime)


def experiment_key(path, regex):
    match = regex.match(path.name)
    if not match:
        return None
    config = match.group("config")
    if match.group("photon"):
        config += "_photon"
    return match.group("bench"), config


def read_pressure(path):
    rows = []
    with path.open(newline="") as f:
        reader = csv.DictReader(f)
        for row in reader:
            start = float(row["cycle_start"])
            end = float(row["cycle_end"])
            rows.append(
                {
                    "cycle": (start + end) / 2.0,
                    "iommu": float(row["shared_mmu_ptw_util"]) * 100.0,
                    "gmmu": float(row["local_gmmu_avg_ptw_util"]) * 100.0,
                }
            )
    return rows


def read_breakdown(path):
    totals = {}
    with path.open(newline="") as f:
        reader = csv.DictReader(f)
        for row in reader:
            path_name = row["path"]
            stage = normalized_breakdown_stage(path_name, row["stage"])
            cycles = float(row["total_cycles"])
            count = int(row["count"])
            totals.setdefault(path_name, {})
            stat = totals[path_name].setdefault(stage, {"cycles": 0.0, "count": 0})
            stat["cycles"] += cycles
            stat["count"] += count
    return totals


def normalized_breakdown_stage(path_name, stage):
    if stage != "gmmutlb_pte_lookup_service":
        return stage
    if path_name == "gmmutlb_hit":
        return "gmmutlb_pte_lookup_hit_service"
    return "gmmutlb_pte_lookup_miss_service"


def bench_label(bench):
    return BENCH_LABELS.get(bench, bench)


def selected_benchmarks(value):
    if not value:
        return None
    return {item.strip() for item in value.split(",") if item.strip()}


def collect_pressure(result_dir, selected=None):
    experiments = []
    for path in sorted(result_dir.glob("*_translation_pressure.csv")):
        key = experiment_key(path, PRESSURE_RE)
        if not key:
            continue
        if selected is not None and key[0] not in selected:
            continue
        rows = read_pressure(path)
        if rows:
            experiments.append((key[0], key[1], rows))
    return experiments


def collect_breakdown(result_dir, selected=None):
    experiments = []
    for path in sorted(result_dir.glob("*_translation_breakdown.csv")):
        key = experiment_key(path, BREAKDOWN_RE)
        if not key:
            continue
        if selected is not None and key[0] not in selected:
            continue
        totals = read_breakdown(path)
        if totals:
            experiments.append((key[0], key[1], totals))
    return experiments


def combine_paths(totals):
    combined = {}
    for stage_totals in totals.values():
        for stage, stat in stage_totals.items():
            combined_stat = combined.setdefault(stage, {"cycles": 0.0, "count": 0})
            combined_stat["cycles"] += stat["cycles"]
            combined_stat["count"] += stat["count"]
    return combined


def full_round_trip_paths(totals):
    return {
        path_name: stage_totals
        for path_name, stage_totals in totals.items()
        if path_name != "gmmutlb_hit"
    }


def breakdown_request_count(stage_totals):
    split_lookup_count = sum(
        stage_totals.get(stage, {"count": 0})["count"]
        for stage in LOOKUP_SPLIT_STAGES
    )
    if split_lookup_count > 0:
        return split_lookup_count
    legacy_lookup = stage_totals.get("gmmutlb_pte_lookup_service")
    if legacy_lookup and legacy_lookup["count"] > 0:
        return legacy_lookup["count"]
    return max((stat["count"] for stat in stage_totals.values()), default=0)


def plot_pressure(experiments, output_base):
    if not experiments:
        raise SystemExit("no pressure CSVs found")

    cols = 2 if len(experiments) > 1 else 1
    rows = math.ceil(len(experiments) / cols)
    fig, axes = plt.subplots(rows, cols, figsize=(6.5 * cols, 3.5 * rows), squeeze=False)
    axes = [ax for row in axes for ax in row]

    for ax, (bench, config, data) in zip(axes, experiments):
        cycles = [row["cycle"] for row in data]
        ax.plot(cycles, [row["iommu"] for row in data], color="#d95f02", lw=2.2, label="IOMMU PTW util")
        ax.plot(cycles, [row["gmmu"] for row in data], color="#1b9e77", lw=2.2, label="Local GMMU PTW avg util")
        ax.set_title(f"{bench_label(bench)} ({config})")
        ax.set_xlabel("Cycle")
        ax.set_ylabel("Utilization (%)")
        ax.set_ylim(0, 105)
        ax.grid(True, axis="y", alpha=0.25)

    for ax in axes[len(experiments) :]:
        ax.axis("off")

    handles, labels = axes[0].get_legend_handles_labels()
    fig.legend(
        handles,
        labels,
        loc="lower center",
        bbox_to_anchor=(0.5, 0.01),
        ncol=2,
        frameon=False,
    )
    fig.suptitle("Translation pressure over time", y=0.99, fontsize=15)
    fig.tight_layout(rect=(0, 0.06, 1, 0.94))
    fig.savefig(output_base.with_suffix(".png"), dpi=220)
    fig.savefig(output_base.with_suffix(".pdf"))
    plt.close(fig)


def plot_breakdown(
    experiments,
    output_base,
    split_paths=False,
    metric="percent",
    full_round_trip_only=False,
):
    if not experiments:
        raise SystemExit("no breakdown CSVs found")

    bar_labels = []
    bar_totals = []
    for bench, config, totals in experiments:
        if full_round_trip_only:
            totals = full_round_trip_paths(totals)
        if split_paths:
            for path_name in PATH_ORDER:
                if full_round_trip_only and path_name == "gmmutlb_hit":
                    continue
                stage_totals = totals.get(path_name, {})
                if not stage_totals:
                    continue
                bar_labels.append(f"{bench_label(bench)}\n{PATH_LABELS[path_name]}")
                bar_totals.append(stage_totals)
        else:
            stage_totals = combine_paths(totals)
            if not stage_totals:
                continue
            bar_labels.append(bench_label(bench))
            bar_totals.append(stage_totals)

    if not bar_labels:
        raise SystemExit("no known breakdown paths found")

    fig, ax = plt.subplots(figsize=(max(11, len(bar_labels) * 1.0), 5.8))
    x = list(range(len(bar_labels)))
    bottoms = [0.0 for _ in bar_labels]

    cycle_denominators = [
        sum(stat["cycles"] for stat in totals.values()) for totals in bar_totals
    ]
    request_denominators = [breakdown_request_count(totals) for totals in bar_totals]
    for stage, label in STAGE_ORDER:
        if metric == "percent":
            values = [
                (totals.get(stage, {"cycles": 0.0})["cycles"] / denom * 100.0)
                if denom
                else 0.0
                for totals, denom in zip(bar_totals, cycle_denominators)
            ]
        elif metric == "per-request":
            values = [
                (totals.get(stage, {"cycles": 0.0})["cycles"] / denom)
                if denom
                else 0.0
                for totals, denom in zip(bar_totals, request_denominators)
            ]
        elif metric == "per-request-percent":
            per_request_totals = [
                (cycle_total / reqs) if reqs else 0.0
                for cycle_total, reqs in zip(cycle_denominators, request_denominators)
            ]
            values = []
            for totals, reqs, per_req_total in zip(
                bar_totals, request_denominators, per_request_totals
            ):
                if not reqs or not per_req_total:
                    values.append(0.0)
                    continue
                stage_per_req = totals.get(stage, {"cycles": 0.0})["cycles"] / reqs
                values.append(stage_per_req / per_req_total * 100.0)
        else:
            values = [
                totals.get(stage, {"cycles": 0.0})["cycles"] / 1_000_000.0
                for totals in bar_totals
            ]
        if not any(values):
            continue
        ax.bar(
            x,
            values,
            bottom=bottoms,
            label=label,
            color=STAGE_COLORS.get(stage),
            width=0.74,
        )
        bottoms = [bottom + value for bottom, value in zip(bottoms, values)]

    ax.set_xticks(x)
    ax.set_xticklabels(bar_labels, rotation=0)
    if metric in ("percent", "per-request-percent"):
        if metric == "per-request-percent":
            ax.set_ylabel("Per-request breakdown (%)")
        else:
            ax.set_ylabel("Breakdown (%)")
        ax.set_ylim(0, 105)
    elif metric == "per-request":
        ax.set_ylabel("Average cycles per request")
    else:
        ax.set_ylabel("Aggregate cycles (M)")
    title = "Translation request breakdown"
    if full_round_trip_only:
        title += " (full round trip only)"
    ax.set_title(title)
    ax.grid(True, axis="y", alpha=0.25)
    ax.legend(loc="upper center", bbox_to_anchor=(0.5, -0.16), ncol=4, frameon=False)
    fig.tight_layout(rect=(0, 0.08, 1, 1))
    fig.savefig(output_base.with_suffix(".png"), dpi=220)
    fig.savefig(output_base.with_suffix(".pdf"))
    plt.close(fig)


def main():
    args = parse_args()
    result_dir = args.result_dir or latest_translation_dir()
    result_dir = result_dir.resolve()
    if not result_dir.is_dir():
        raise SystemExit(f"result directory does not exist: {result_dir}")

    selected = selected_benchmarks(args.benchmarks)
    pressure = collect_pressure(result_dir, selected)
    breakdown = collect_breakdown(result_dir, selected)

    pressure_base = result_dir / f"{args.output_prefix}_pressure_utilization"
    suffix = {
        "percent": "percent",
        "per-request": "per_request",
        "per-request-percent": "per_request_percent",
        "cycles": "cycles",
    }[args.breakdown_metric]
    if args.full_round_trip_only:
        suffix += "_full_round_trip"
    breakdown_base = result_dir / f"{args.output_prefix}_breakdown_{suffix}"
    plot_pressure(pressure, pressure_base)
    plot_breakdown(
        breakdown,
        breakdown_base,
        split_paths=args.split_paths,
        metric=args.breakdown_metric,
        full_round_trip_only=args.full_round_trip_only,
    )

    print(pressure_base.with_suffix(".png"))
    print(pressure_base.with_suffix(".pdf"))
    print(breakdown_base.with_suffix(".png"))
    print(breakdown_base.with_suffix(".pdf"))


if __name__ == "__main__":
    main()
