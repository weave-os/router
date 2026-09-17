import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "build_v076_gpt56_deepseek_overlay.py"
FIXTURE = ROOT / "scripts" / "testdata" / "v076_overlay"
FIXTURE_PARENT = FIXTURE / "parent"
FIXTURE_MANIFEST = FIXTURE / "measurements.json"

spec = importlib.util.spec_from_file_location("v076_overlay", SCRIPT)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


def load_json(path):
    return json.loads(path.read_text(encoding="utf-8"))


class OverlayBuilderTest(unittest.TestCase):
    def valid_manifest(self):
        return copy.deepcopy(load_json(FIXTURE_MANIFEST))

    def test_rejects_quality_vector_with_wrong_cluster_count(self):
        data = self.valid_manifest()
        data["models"]["gpt-5.6-luna"]["quality"] = [0.42]
        with self.assertRaisesRegex(ValueError, r"gpt-5.6-luna.*quality.*2"):
            module.validate_measurements(data, 2)

    def test_rejects_non_finite_quality(self):
        data = self.valid_manifest()
        data["models"]["gpt-5.6-luna"]["quality"][0] = float("nan")
        with self.assertRaisesRegex(ValueError, r"gpt-5.6-luna.*quality.*finite"):
            module.validate_measurements(data, 2)

    def test_rejects_measured_entry_without_cluster_samples(self):
        data = self.valid_manifest()
        del data["models"]["gpt-5.6-luna"]["cluster_samples"]
        with self.assertRaisesRegex(ValueError, r"gpt-5.6-luna.*cluster_samples"):
            module.validate_measurements(data, 2)

    def test_rejects_silent_proxy(self):
        data = self.valid_manifest()
        entry = data["models"]["gpt-5.6-luna"]
        entry["evidence"] = "proxy"
        entry["source_model"] = "stable/model"
        entry.pop("quality")
        entry.pop("cluster_samples")
        with self.assertRaisesRegex(ValueError, r"gpt-5.6-luna.*proxy_reason"):
            module.validate_measurements(data, 2)

    def test_rejects_missing_operational_axis(self):
        data = self.valid_manifest()
        del data["models"]["gpt-5.6-luna"]["operational"]["tps"]
        with self.assertRaisesRegex(ValueError, r"gpt-5.6-luna.*tps"):
            module.validate_measurements(data, 2)

    def test_rejects_non_positive_price_or_speed(self):
        for field in ("input_per_1k_usd", "output_per_1k_usd", "ttft_s", "tps"):
            with self.subTest(field=field):
                data = self.valid_manifest()
                data["models"]["gpt-5.6-luna"]["operational"][field] = 0
                with self.assertRaisesRegex(ValueError, rf"gpt-5.6-luna.*{field}.*positive"):
                    module.validate_measurements(data, 2)

    def test_rejects_existing_output_without_overwrite(self):
        with tempfile.TemporaryDirectory() as tmp:
            output = Path(tmp) / "v0.76"
            output.mkdir()
            (output / "rankings.json").write_text("{}\n", encoding="utf-8")
            with self.assertRaisesRegex(FileExistsError, "--overwrite"):
                module.build_bundle(FIXTURE_PARENT, FIXTURE_MANIFEST, output, overwrite=False)

    def test_validate_inputs_reports_manifest_and_cluster_count(self):
        manifest, k = module.validate_inputs(FIXTURE_PARENT, FIXTURE_MANIFEST)
        self.assertEqual(k, 2)
        self.assertEqual(len(manifest["models"]), 3)

    def test_calibrate_cluster_interpolates_between_sorted_anchors(self):
        self.assertAlmostEqual(module.calibrate_cluster(0.60, [(0.50, 0.40), (0.70, 0.80)]), 0.60)

    def test_calibrate_cluster_clamps_below_and_above_anchor_range(self):
        anchors = [(0.50, 0.40), (0.70, 0.80)]
        self.assertEqual(module.calibrate_cluster(0.10, anchors), 0.40)
        self.assertEqual(module.calibrate_cluster(0.90, anchors), 0.80)

    def test_calibrate_cluster_rejects_fewer_than_two_distinct_scores(self):
        with self.assertRaisesRegex(ValueError, "distinct"):
            module.calibrate_cluster(0.5, [(0.5, 0.2), (0.5, 0.4)])

    def test_blend_rankings_reproduces_parent_fixture(self):
        quality = load_json(FIXTURE_PARENT / "quality_means.json")["quality_means"]
        axes = load_json(FIXTURE_PARENT / "model_axes.json")["axes"]
        rankings = load_json(FIXTURE_PARENT / "rankings.json")
        self.assertEqual(module.blend_rankings(quality, axes, rankings["meta"]), rankings["rankings"])

    def test_blend_rankings_reproduces_real_v075(self):
        parent = ROOT / "internal" / "router" / "cluster" / "artifacts" / "v0.75"
        quality = load_json(parent / "quality_means.json")["quality_means"]
        axes = load_json(parent / "model_axes.json")["axes"]
        rankings = load_json(parent / "rankings.json")
        actual = module.blend_rankings(quality, axes, rankings["meta"])
        for cluster, row in rankings["rankings"].items():
            for model, expected in row.items():
                self.assertAlmostEqual(actual[cluster][model], expected, delta=1e-6)

    def test_replace_top_level_yaml_block_preserves_neighbors(self):
        text = "first: one\ntarget:\n  old: true\nlast:\n  keep: exact\n"
        actual = module.replace_top_level_yaml_block(text, "target", "target:\n  new: true\n")
        self.assertEqual(actual, "first: one\ntarget:\n  new: true\nlast:\n  keep: exact\n")

    def test_replace_top_level_yaml_block_rejects_missing_or_duplicate_key(self):
        with self.assertRaisesRegex(ValueError, "exactly once"):
            module.replace_top_level_yaml_block("other: true\n", "target", "target: new\n")
        with self.assertRaisesRegex(ValueError, "exactly once"):
            module.replace_top_level_yaml_block("target: one\ntarget: two\n", "target", "target: new\n")

    def test_build_bundle_accepts_review_first_input_only_directory(self):
        with tempfile.TemporaryDirectory() as tmp:
            output = Path(tmp) / "v0.76"
            inputs = output / "inputs"
            inputs.mkdir(parents=True)
            manifest = inputs / "model_measurements.json"
            manifest.write_bytes(FIXTURE_MANIFEST.read_bytes())
            module.build_bundle(FIXTURE_PARENT, manifest, output, overwrite=False)
            self.assertTrue((output / "rankings.json").is_file())
            self.assertEqual((output / "inputs" / "model_measurements.json").read_bytes(), FIXTURE_MANIFEST.read_bytes())

    def test_build_bundle_is_complete_consistent_and_deterministic(self):
        with tempfile.TemporaryDirectory() as tmp:
            first = Path(tmp) / "first"
            second = Path(tmp) / "second"
            module.build_bundle(FIXTURE_PARENT, FIXTURE_MANIFEST, first, overwrite=False)
            module.build_bundle(FIXTURE_PARENT, FIXTURE_MANIFEST, second, overwrite=False)

            self.assertEqual((first / "centroids.bin").read_bytes(), (FIXTURE_PARENT / "centroids.bin").read_bytes())
            self.assertFalse((first / "latest").exists())
            registry = load_json(first / "model_registry.json")
            roster = {item["model"] for item in registry["deployed_models"]}
            self.assertNotIn("deepseek/deepseek-v4-pro", roster)
            self.assertIn("deepseek/deepseek-v4-pro-0813", roster)
            self.assertIn("gpt-5.6-luna", roster)

            quality = load_json(first / "quality_means.json")["quality_means"]
            axes = load_json(first / "model_axes.json")["axes"]
            features = load_json(first / "model_features.json")["models"]
            rankings = load_json(first / "rankings.json")["rankings"]
            for row in quality.values():
                self.assertEqual(set(row), roster)
            self.assertEqual(set(axes), roster)
            self.assertEqual(set(features), roster)
            self.assertEqual({model for row in rankings.values() for model in row}, roster)
            for model in roster:
                self.assertEqual(features[model]["psi_probe"], [quality[str(i)][model] for i in range(2)])

            metadata = (first / "metadata.yaml").read_text(encoding="utf-8")
            self.assertIn("version: v0.76\n", metadata)
            self.assertIn("parent: v0.75\n", metadata)
            self.assertIn("status: candidate\n", metadata)
            self.assertIn("unknown_block:\n  preserve: exactly\n", metadata)
            self.assertIn("generator: build_v076_gpt56_deepseek_overlay.py\n", metadata)
            self.assertRegex(metadata, r"centroid_sha256: [0-9a-f]{64}\n")
            self.assertRegex(metadata, r"manifest_sha256: [0-9a-f]{64}\n")
            for path in sorted(first.rglob("*")):
                if path.is_file():
                    relative = path.relative_to(first)
                    self.assertEqual(path.read_bytes(), (second / relative).read_bytes(), str(relative))


if __name__ == "__main__":
    unittest.main()
