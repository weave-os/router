#!/usr/bin/env python3
"""Build the reviewed v0.76 model overlay on frozen v0.75 geometry."""

import argparse
import copy
import hashlib
import json
import math
from pathlib import Path
import shutil
import struct
import tempfile


VALID_EVIDENCE = {"measured", "same-upstream-alias", "proxy"}
REQUIRED_OPERATIONAL = {
    "input_per_1k_usd",
    "output_per_1k_usd",
    "ttft_s",
    "tps",
    "verbosity_tokens",
}
ARTIFACT_FILES = {
    "centroids.bin",
    "model_registry.json",
    "quality_means.json",
    "model_axes.json",
    "model_features.json",
    "rankings.json",
    "metadata.yaml",
}


def load_json(path: Path) -> dict:
    with path.open(encoding="utf-8") as handle:
        return json.load(handle)


def load_measurements(path: Path) -> dict:
    data = load_json(path)
    if not isinstance(data, dict):
        raise ValueError("measurement manifest must be a JSON object")
    return data


def _finite_number(value) -> bool:
    return isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(value)


def validate_measurements(data: dict, k: int) -> None:
    if data.get("schema_version") != 1:
        raise ValueError("schema_version must be 1")
    if data.get("version") != "v0.76" or data.get("parent") != "v0.75":
        raise ValueError("manifest must describe v0.75 -> v0.76")
    models = data.get("models")
    if not isinstance(models, dict) or not models:
        raise ValueError("models must be a non-empty object")
    remove_models = data.get("remove_models")
    if not isinstance(remove_models, list) or any(not isinstance(model, str) for model in remove_models) or len(remove_models) != len(set(remove_models)):
        raise ValueError("remove_models must be a duplicate-free list")

    overlap = sorted(set(models) & set(remove_models))
    if overlap:
        raise ValueError(f"model cannot be both added and removed: {overlap[0]}")

    for model in sorted(models):
        entry = models[model]
        if not isinstance(entry, dict):
            raise ValueError(f"{model}: entry must be an object")
        evidence = entry.get("evidence")
        if evidence not in VALID_EVIDENCE:
            raise ValueError(f"{model}: evidence must be one of {sorted(VALID_EVIDENCE)}")
        for field in ("provider", "bench_column", "source"):
            if not isinstance(entry.get(field), str) or not entry[field].strip():
                raise ValueError(f"{model}: {field} must be a non-empty string")

        if evidence == "measured":
            quality = entry.get("quality")
            if not isinstance(quality, list) or len(quality) != k:
                raise ValueError(f"{model}: quality must contain exactly {k} values")
            if any(not _finite_number(value) for value in quality):
                raise ValueError(f"{model}: quality values must be finite")
            samples = entry.get("cluster_samples")
            if not isinstance(samples, list) or len(samples) != k:
                raise ValueError(f"{model}: cluster_samples must contain exactly {k} values")
            if any(not isinstance(value, int) or isinstance(value, bool) or value <= 0 for value in samples):
                raise ValueError(f"{model}: cluster_samples must be positive integers")
        else:
            if not isinstance(entry.get("source_model"), str) or not entry["source_model"].strip():
                raise ValueError(f"{model}: source_model must be a non-empty string")
            if evidence == "proxy" and (not isinstance(entry.get("proxy_reason"), str) or not entry["proxy_reason"].strip()):
                raise ValueError(f"{model}: proxy_reason must be a non-empty string")

        operational = entry.get("operational")
        if not isinstance(operational, dict):
            raise ValueError(f"{model}: operational must be an object")
        for field in sorted(REQUIRED_OPERATIONAL):
            if field not in operational:
                raise ValueError(f"{model}: operational field {field} is required")
        for field in ("input_per_1k_usd", "output_per_1k_usd", "ttft_s", "tps"):
            value = operational[field]
            if not _finite_number(value) or value <= 0:
                raise ValueError(f"{model}: {field} must be finite and positive")
        verbosity = operational["verbosity_tokens"]
        if verbosity is not None and (not _finite_number(verbosity) or verbosity < 0):
            raise ValueError(f"{model}: verbosity_tokens must be null or finite and non-negative")


def calibrate_cluster(observed: float, anchors: list[tuple[float, float]]) -> float:
    if not _finite_number(observed):
        raise ValueError("observed score must be finite")
    grouped = {}
    for source, target in anchors:
        if not _finite_number(source) or not _finite_number(target):
            raise ValueError("anchors must be finite")
        grouped.setdefault(source, []).append(target)
    if len(grouped) < 2:
        raise ValueError("calibration needs at least two distinct observed anchor scores")

    points = []
    target_floor = -math.inf
    for source in sorted(grouped):
        target = sum(grouped[source]) / len(grouped[source])
        target_floor = max(target_floor, target)
        points.append((source, target_floor))
    if observed <= points[0][0]:
        return points[0][1]
    if observed >= points[-1][0]:
        return points[-1][1]
    for (left_x, left_y), (right_x, right_y) in zip(points, points[1:]):
        if observed <= right_x:
            ratio = (observed - left_x) / (right_x - left_x)
            return min(left_y + ratio * (right_y - left_y), points[-1][1])
    raise AssertionError("unreachable calibration range")


def _f32(value: float) -> float:
    return struct.unpack("<f", struct.pack("<f", value))[0]


def _f32_add(left: float, right: float) -> float:
    return _f32(_f32(left) + _f32(right))


def _f32_sub(left: float, right: float) -> float:
    return _f32(_f32(left) - _f32(right))


def _f32_mul(left: float, right: float) -> float:
    return _f32(_f32(left) * _f32(right))


def _alpha_for(meta: dict, cluster: int) -> float:
    alpha = meta["alpha"]
    return alpha[cluster] if isinstance(alpha, list) else alpha


def blend_rankings(quality: dict, axes: dict, knobs: dict) -> dict:
    models = sorted(axes)
    quality_models = {model for row in quality.values() for model in row}
    if quality_models != set(models) or any(set(row) != set(models) for row in quality.values()):
        raise ValueError("quality and axes model sets must match")

    verbosity_values = sorted(
        axis["verbosity_tokens"]
        for axis in axes.values()
        if axis.get("verbosity_tokens") is not None
    )
    median_verbosity = verbosity_values[len(verbosity_values) // 2] if verbosity_values else 0
    output_ratio = knobs["output_cost_ratio"]
    costs = {}
    speeds = {}
    for model in models:
        axis = axes[model]
        verbosity_factor = 1.0
        if knobs["per_model_verbosity"] and axis.get("verbosity_tokens") is not None and median_verbosity > 0:
            verbosity_factor = axis["verbosity_tokens"] / median_verbosity
        costs[model] = (axis.get("input_per_1k_usd") or 0.0) + output_ratio * (axis.get("output_per_1k_usd") or 0.0) * verbosity_factor
        ttft = axis.get("ttft_s")
        tps = axis.get("tps")
        speeds[model] = ttft + knobs["expected_output_tokens"] / tps if ttft is not None and tps is not None and tps > 0 else None

    c_min, c_max = min(costs.values()), max(costs.values())
    c_range = c_max - c_min
    use_speed = knobs["speed_weight"] > 0
    timed = [value for value in speeds.values() if value is not None] if use_speed else []
    s_min, s_max = (min(timed), max(timed)) if timed else (0.0, 0.0)
    s_range = s_max - s_min

    rankings = {}
    for cluster_text in sorted(quality, key=int):
        cluster = int(cluster_text)
        row = {model: _f32(value) for model, value in quality[cluster_text].items()}
        q_min, q_max = min(row.values()), max(row.values())
        q_range = _f32_sub(q_max, q_min)
        w_q = _f32(_alpha_for(knobs, cluster))
        w_s = _f32(knobs["speed_weight"]) if use_speed else _f32(0)
        w_c = _f32_sub(_f32_sub(1.0, w_q), w_s)
        output = {}
        for model in models:
            q_norm = _f32(0)
            if q_range > 0:
                q_norm = _f32(_f32_sub(row[model], q_min) / q_range)
            c_norm = _f32(0)
            if c_range > 0:
                c_norm = _f32((costs[model] - c_min) / c_range)
            if s_range > 0:
                s_norm = _f32(1)
                if speeds[model] is not None:
                    s_norm = _f32((speeds[model] - s_min) / s_range)
                blend = _f32_add(_f32_add(_f32_mul(w_q, q_norm), _f32_mul(w_c, _f32_sub(1, c_norm))), _f32_mul(w_s, _f32_sub(1, s_norm)))
            else:
                total = _f32_add(w_q, w_c)
                if total > 0:
                    blend = _f32_add(_f32_mul(_f32(w_q / total), q_norm), _f32_mul(_f32(w_c / total), _f32_sub(1, c_norm)))
                else:
                    blend = q_norm
            output[model] = blend
        rankings[cluster_text] = output
    return rankings


def replace_top_level_yaml_block(text: str, key: str, replacement: str) -> str:
    lines = text.splitlines(keepends=True)
    starts = [index for index, line in enumerate(lines) if line.startswith(f"{key}:")]
    if len(starts) != 1:
        raise ValueError(f"metadata key {key} must occur exactly once")
    start = starts[0]
    end = len(lines)
    for index in range(start + 1, len(lines)):
        line = lines[index]
        if line and not line[0].isspace() and ":" in line:
            end = index
            break
    if replacement and not replacement.endswith("\n"):
        replacement += "\n"
    return "".join(lines[:start]) + replacement + "".join(lines[end:])


def _json_bytes(payload: dict) -> bytes:
    return (json.dumps(payload, indent=2, sort_keys=True, allow_nan=False) + "\n").encode()


def _yaml_list(key: str, values: list[str]) -> str:
    return f"{key}:\n" + "".join(f"- {value}\n" for value in values)


def _yaml_map(key: str, values: dict[str, float]) -> str:
    return f"{key}:\n" + "".join(f"  {name}: {json.dumps(values[name], allow_nan=False)}\n" for name in sorted(values))


def _validate_parent_sources(manifest: dict, parent_quality: dict) -> None:
    for model, entry in sorted(manifest["models"].items()):
        if entry["evidence"] == "measured":
            continue
        source = entry["source_model"]
        if any(source not in row for row in parent_quality.values()):
            raise ValueError(f"{model}: source_model {source} must exist in every parent cluster")


def validate_inputs(parent: Path, measurements: Path) -> tuple[dict, int]:
    quality = load_json(parent / "quality_means.json")["quality_means"]
    k = len(quality)
    if set(quality) != {str(index) for index in range(k)}:
        raise ValueError("parent quality cluster keys must be contiguous from zero")
    manifest = load_measurements(measurements)
    validate_measurements(manifest, k)
    _validate_parent_sources(manifest, quality)
    return manifest, k


def _assert_projection_consistency(registry: dict, quality: dict, axes: dict, features: dict, rankings: dict) -> None:
    roster = {item["model"] for item in registry["deployed_models"]}
    structures = [("quality", set(row)) for row in quality["quality_means"].values()]
    structures += [("axes", set(axes["axes"])), ("features", set(features["models"]))]
    structures += [("rankings", set(row)) for row in rankings["rankings"].values()]
    for name, models in structures:
        if models != roster:
            raise ValueError(f"{name} model set does not match registry")
    k = len(quality["quality_means"])
    for model in sorted(roster):
        expected = [quality["quality_means"][str(index)][model] for index in range(k)]
        if features["models"][model]["psi_probe"] != expected:
            raise ValueError(f"{model}: psi_probe does not match quality_means")


def _output_is_input_only(output: Path, measurements: Path) -> bool:
    if not output.exists():
        return False
    entries = sorted(path.relative_to(output).as_posix() for path in output.rglob("*") if path.is_file())
    expected = output / "inputs" / "model_measurements.json"
    return entries == ["inputs/model_measurements.json"] and expected.resolve() == measurements.resolve()


def build_bundle(parent: Path, measurements: Path, output: Path, overwrite: bool) -> None:
    parent = parent.resolve()
    measurements = measurements.resolve()
    output = output.resolve()
    input_only = _output_is_input_only(output, measurements)
    if output.exists() and not overwrite and not input_only:
        raise FileExistsError(f"{output} exists; pass --overwrite to replace it")

    registry = copy.deepcopy(load_json(parent / "model_registry.json"))
    quality = copy.deepcopy(load_json(parent / "quality_means.json"))
    axes = copy.deepcopy(load_json(parent / "model_axes.json"))
    rankings_parent = load_json(parent / "rankings.json")
    manifest, k = validate_inputs(parent, measurements)
    parent_quality = copy.deepcopy(quality["quality_means"])

    removed = set(manifest["remove_models"])
    registry["deployed_models"] = [item for item in registry["deployed_models"] if item["model"] not in removed]
    for row in quality["quality_means"].values():
        for model in removed:
            row.pop(model, None)
    for model in removed:
        axes["axes"].pop(model, None)

    registry_by_model = {item["model"]: item for item in registry["deployed_models"]}
    for model, entry in sorted(manifest["models"].items()):
        registry_entry = {
            "model": model,
            "provider": entry["provider"],
            "bench_column": entry["bench_column"],
            "direct_label": "routerarena",
        }
        if entry["evidence"] == "proxy":
            registry_entry.update(proxy=True, proxy_note=entry["proxy_reason"])
        elif entry["evidence"] == "same-upstream-alias":
            registry_entry.update(proxy=True, proxy_note=f"Same upstream as {entry['source_model']}; quality retained from reviewed parent cells.")
        registry_by_model[model] = registry_entry
        values = entry["quality"] if entry["evidence"] == "measured" else [parent_quality[str(index)][entry["source_model"]] for index in range(k)]
        for index, value in enumerate(values):
            quality["quality_means"][str(index)][model] = value
        axes["axes"][model] = copy.deepcopy(entry["operational"])
    registry["deployed_models"] = [registry_by_model[model] for model in sorted(registry_by_model)]

    roster = sorted(registry_by_model)
    quality["meta"]["k"] = k
    quality["meta"]["roster_version"] = "v0.76"
    axes.setdefault("meta", {})["k"] = k
    axes["meta"]["roster_version"] = "v0.76"
    features = {
        "meta": {
            "comment": "v0.76 reviewed model overlay on byte-identical v0.75 cluster geometry.",
            "k": k,
            "n_models": len(roster),
            "roster_version": "v0.76",
            "source": "quality_means.json + model_axes.json",
        },
        "models": {
            model: {
                "operational": copy.deepcopy(axes["axes"][model]),
                "psi_probe": [quality["quality_means"][str(index)][model] for index in range(k)],
            }
            for model in roster
        },
    }
    ranking_meta = copy.deepcopy(rankings_parent["meta"])
    ranking_meta.update(
        k=k,
        roster_version="v0.76",
        parent="v0.75",
        regenerated_by="build_v076_gpt56_deepseek_overlay.py",
        measured_axes_source="inputs/model_measurements.json",
        cost_per_1k_input_usd={model: axes["axes"][model]["input_per_1k_usd"] for model in roster},
    )
    rankings = {
        "meta": ranking_meta,
        "rankings": blend_rankings(quality["quality_means"], axes["axes"], ranking_meta),
    }
    _assert_projection_consistency(registry, quality, axes, features, rankings)

    metadata = (parent / "metadata.yaml").read_text(encoding="utf-8")
    replacements = {
        "version": "version: v0.76\n",
        "parent": "parent: v0.75\n",
        "status": "status: candidate\n",
        "deployed_providers": _yaml_list("deployed_providers", sorted({entry["provider"] for entry in registry["deployed_models"]})),
        "deployed_models": _yaml_list("deployed_models", roster),
        "cost_per_1k_input_usd": _yaml_map("cost_per_1k_input_usd", ranking_meta["cost_per_1k_input_usd"]),
        "changelog": "changelog: v0.76 reviewed model overlay on byte-identical v0.75 centroids; latest remains unchanged.\n",
    }
    for key, replacement in replacements.items():
        metadata = replace_top_level_yaml_block(metadata, key, replacement)
    centroid_sha256 = hashlib.sha256((parent / "centroids.bin").read_bytes()).hexdigest()
    manifest_sha256 = hashlib.sha256(measurements.read_bytes()).hexdigest()
    metadata += (
        "overlay_evidence:\n"
        "  manifest: inputs/model_measurements.json\n"
        "  generator: build_v076_gpt56_deepseek_overlay.py\n"
        "  geometry: byte-identical-v0.75\n"
        f"  centroid_sha256: {centroid_sha256}\n"
        f"  manifest_sha256: {manifest_sha256}\n"
    )

    output.parent.mkdir(parents=True, exist_ok=True)
    manifest_bytes = measurements.read_bytes()
    temp = Path(tempfile.mkdtemp(prefix=f".{output.name}.tmp-", dir=output.parent))
    backup = output.with_name(f".{output.name}.backup")
    try:
        (temp / "inputs").mkdir()
        (temp / "inputs" / "model_measurements.json").write_bytes(manifest_bytes)
        shutil.copyfile(parent / "centroids.bin", temp / "centroids.bin")
        for name, payload in (
            ("model_registry.json", registry),
            ("quality_means.json", quality),
            ("model_axes.json", axes),
            ("model_features.json", features),
            ("rankings.json", rankings),
        ):
            (temp / name).write_bytes(_json_bytes(payload))
        (temp / "metadata.yaml").write_text(metadata, encoding="utf-8")
        if {path.name for path in temp.iterdir()} != ARTIFACT_FILES | {"inputs"}:
            raise ValueError("generated bundle has an unexpected file set")

        if backup.exists():
            raise FileExistsError(f"atomic backup path already exists: {backup}")
        if output.exists():
            output.rename(backup)
        try:
            temp.rename(output)
        except Exception:
            if backup.exists() and not output.exists():
                backup.rename(output)
            raise
        if backup.exists():
            shutil.rmtree(backup)
    finally:
        if temp.exists():
            shutil.rmtree(temp)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--parent", type=Path, required=True)
    parser.add_argument("--measurements", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--overwrite", action="store_true")
    parser.add_argument("--validate-only", action="store_true")
    args = parser.parse_args()
    if args.validate_only:
        manifest, k = validate_inputs(args.parent, args.measurements)
        print(f"validated v0.76 measurements: {len(manifest['models'])} entries, k={k}")
        return
    build_bundle(args.parent, args.measurements, args.output, args.overwrite)


if __name__ == "__main__":
    main()
