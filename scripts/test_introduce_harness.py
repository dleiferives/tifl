#!/usr/bin/env python3
"""Offline coverage for Introduce-with-context harness behavior.

Run from the repo root:
    python3 scripts/test_introduce_harness.py

This test uses static fixtures only. It does not read tifl.yaml, call
OpenRouter, invoke the Claude grader, or require network access.
"""

import importlib.util
import json
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
FIXTURES = ROOT / "scripts" / "prompts" / "introduce_harness_fixtures.json"
PIPELINE = ROOT / "scripts" / "prompts" / "pipelines" / "bigpool_skills.json"


def load_script(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / "scripts" / f"{name}.py")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


pd = load_script("prompt_dag")
score = load_script("score")


class IntroduceHarnessTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.doc = json.loads(FIXTURES.read_text())
        cls.level = cls.doc["levels"][0]
        cls.scenario = cls.doc["scenarios"][0]
        cls.fixtures = cls.doc["fixtures"]

    def test_rendered_prompt_includes_introduce_target_construction(self):
        pipeline = json.loads(PIPELINE.read_text())
        ctx = pd.build_context(self.scenario, self.doc, self.level)
        user = pd.render(pipeline["steps"][0]["user"], ctx)

        self.assertIn("Εισήγαγε με σαφή υποστήριξη από τα συμφραζόμενα", user)
        self.assertIn("γενική πτώση για απλή κατοχή", user)
        self.assertIn("ποδήλατο", user)

    def test_introduce_check_accepts_supported_construction(self):
        ok, detail = pd.check_introduce_constraints(self.fixtures["supported"], self.scenario)

        self.assertTrue(ok, detail)
        self.assertEqual([], detail["missing"])
        self.assertEqual([], detail["unsupported"])

    def test_introduce_check_rejects_missing_construction(self):
        ok, detail = pd.check_introduce_constraints(
            self.fixtures["missing_construction"], self.scenario
        )

        self.assertFalse(ok)
        self.assertEqual(["γενική πτώση για απλή κατοχή"], detail["missing"])
        self.assertEqual([], detail["unsupported"])

    def test_introduce_check_rejects_unsupported_construction(self):
        ok, detail = pd.check_introduce_constraints(
            self.fixtures["unsupported_construction"], self.scenario
        )

        self.assertFalse(ok)
        self.assertEqual([], detail["missing"])
        self.assertEqual("γενική πτώση για απλή κατοχή", detail["unsupported"][0]["construction"])

    def test_score_exports_introduce_metrics(self):
        supported = score.deterministic(self.fixtures["supported"], self.scenario)
        unsupported = score.deterministic(self.fixtures["unsupported_construction"], self.scenario)

        self.assertEqual([], supported["introduce_missing"])
        self.assertEqual([], supported["introduce_unsupported"])
        self.assertEqual([], unsupported["introduce_missing"])
        self.assertEqual(
            "γενική πτώση για απλή κατοχή",
            unsupported["introduce_unsupported"][0]["construction"],
        )


if __name__ == "__main__":
    unittest.main()
