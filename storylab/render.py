"""Jinja rendering + expression evaluation for the DAG engine.

One Jinja environment does double duty:
  * renders prompt templates (files in prompts/ or inline strings in an arch), and
  * evaluates node conditions — `when:`, `foreach:`, `loop.until:` — via
    compile_expression. So there is exactly one expression language for authors,
    not a template syntax plus a separate condition DSL.

Templates and conditions see the blackboard (spec, level, every upstream node's
output) plus a few helper functions bound to the current run: `coverage(text)`,
`metrics(text)`, `target` (the level's coverage target), and a `json` filter
that keeps Greek readable (ensure_ascii=False).
"""
from __future__ import annotations

import json
import re
from pathlib import Path
from typing import Any

from jinja2 import Environment, FileSystemLoader, meta

from backend.core.coverage import coverage as _coverage
from backend.core.prompts import _JSON_RULES

from storylab.metrics import story_metrics

PROMPTS_DIR = Path(__file__).resolve().parent / "prompts"

_env = Environment(
    loader=FileSystemLoader(str(PROMPTS_DIR)),
    trim_blocks=True,
    lstrip_blocks=True,
    keep_trailing_newline=False,
)
_env.filters["json"] = lambda x: json.dumps(x, ensure_ascii=False)
_env.globals["json_rules"] = _JSON_RULES


def _is_file_ref(prompt: str) -> bool:
    """A bare identifier naming a file in prompts/ — vs an inline template body."""
    return bool(re.fullmatch(r"[\w./-]+", prompt.strip())) and (PROMPTS_DIR / f"{prompt.strip()}.j2").exists()


def _helpers(known_chunks: list[str], target: float) -> dict[str, Any]:
    return {
        "coverage": lambda text: _coverage(text or "", known_chunks),
        "metrics": lambda text: story_metrics(text or "", known_chunks),
        "target": target,
        "len": len,
        "min": min,
        "max": max,
    }


def render_prompt(prompt: str, ctx: dict[str, Any], helpers: dict[str, Any]) -> str:
    """Render a prompt — a file reference or an inline template string."""
    full = {**ctx, **helpers}
    if _is_file_ref(prompt):
        tmpl = _env.get_template(f"{prompt.strip()}.j2")
    else:
        tmpl = _env.from_string(prompt)
    return tmpl.render(**full).strip()


def eval_expr(expr: str, ctx: dict[str, Any], helpers: dict[str, Any]) -> Any:
    """Evaluate a Jinja expression (used for when/foreach/until conditions)."""
    compiled = _env.compile_expression(expr)
    return compiled(**{**ctx, **helpers})


def referenced_names(prompt: str, *exprs: str) -> set[str]:
    """Identifiers a node's prompt + conditions reference — used to infer edges.

    For a file/inline template we use Jinja's own parser; for plain expressions
    we fall back to a word scan (compile_expression has no public AST walk).
    """
    names: set[str] = set()
    src = (PROMPTS_DIR / f"{prompt.strip()}.j2").read_text(encoding="utf-8") if _is_file_ref(prompt) else prompt
    names |= meta.find_undeclared_variables(_env.parse(src))
    for expr in exprs:
        if expr:
            names |= set(re.findall(r"[A-Za-z_]\w*", expr))
    return names
