"""Arch = a DAG of LLM nodes, authored in YAML.

A node produces a value stored on the blackboard under its id; downstream nodes
read upstream outputs (by id) in their prompts and conditions. Edges are the
union of explicit `needs` and the node ids a node's prompt/conditions reference,
so the common case needs no `needs:` at all. The graph must be acyclic — loops
live *inside* a node (`loop:`), not as back-edges.

Two node types:
  generate  render a prompt, call the LLM, store the (optionally extracted) output.
            modifiers: foreach (fan-out), when (conditional), loop (bounded
            internal loop), merge (promote output fields to shared context).
  select    deterministic fan-in: pick one item `from` a fan-out list `by` a
            metric (e.g. coverage) or by the LLM judge.
"""
from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import yaml

from storylab.render import referenced_names

ARCHES_DIR = Path(__file__).resolve().parent / "arches"
LENSES_PATH = Path(__file__).resolve().parent / "lenses.yaml"


@dataclass
class Node:
    id: str
    type: str = "generate"          # "generate" | "select"
    prompt: str = ""                # template name or inline body (generate)
    needs: list[str] = field(default_factory=list)
    when: str | None = None         # Jinja expr; skip node if falsey
    foreach: str | None = None      # Jinja expr -> list; fan-out per item
    loop: dict[str, Any] | None = None   # {until,max} or {cycle:[lenses],passes}
    merge: list[str] = field(default_factory=list)  # output keys to lift to shared ctx
    extract: str | None = None      # pull this field out of parsed JSON
    parse: str = "json"             # "json" | "text"
    optional: bool = False          # if the call fails, skip instead of aborting
    # select-only:
    src: str | None = None          # node id producing the candidate list ("from")
    by: str = "coverage"            # "coverage" | "ttr" | ... | "judge"

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> "Node":
        return cls(
            id=d["id"],
            type=d.get("type", "generate"),
            prompt=d.get("prompt", ""),
            needs=list(d.get("needs") or []),
            when=d.get("when"),
            foreach=d.get("foreach"),
            loop=d.get("loop"),
            merge=list(d.get("merge") or []),
            extract=d.get("extract"),
            parse=d.get("parse", "json"),
            optional=bool(d.get("optional", False)),
            src=d.get("from"),
            by=d.get("by", "coverage"),
        )

    def dependencies(self, node_ids: set[str]) -> set[str]:
        """Edges into this node: explicit needs + referenced ids + select source."""
        deps: set[str] = set(self.needs)
        if self.src:
            deps.add(self.src)
        exprs = [e for e in (self.when, self.foreach) if e]
        if self.loop:
            exprs += [str(self.loop.get("until", ""))]
        if self.type == "generate" and self.prompt:
            deps |= referenced_names(self.prompt, *exprs) & node_ids
        deps.discard(self.id)
        return deps


@dataclass
class Arch:
    id: str
    description: str
    result: str                     # node id whose output is the final story
    result_extract: str | None      # field to pull from that output ("text")
    nodes: list[Node]

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> "Arch":
        return cls(
            id=d["id"],
            description=d.get("description", ""),
            result=d["result"],
            result_extract=d.get("result_extract"),
            nodes=[Node.from_dict(n) for n in d["nodes"]],
        )

    def signature(self) -> dict[str, Any]:
        """Output-affecting shape, for cache keys (template text hashed separately)."""
        return {
            "result": self.result, "result_extract": self.result_extract,
            "nodes": [n.__dict__ for n in self.nodes],
        }

    def template_names(self) -> set[str]:
        return {n.prompt for n in self.nodes if n.type == "generate" and n.prompt}

    def topo_order(self) -> list[list[Node]]:
        """Kahn's algorithm, grouped into levels that can run in parallel."""
        ids = {n.id for n in self.nodes}
        by_id = {n.id: n for n in self.nodes}
        deps = {n.id: n.dependencies(ids) for n in self.nodes}
        levels: list[list[Node]] = []
        done: set[str] = set()
        while len(done) < len(self.nodes):
            ready = [nid for nid in ids if nid not in done and deps[nid] <= done]
            if not ready:
                raise ValueError(f"arch {self.id!r}: cycle or missing dep among {ids - done}")
            ready.sort()  # stable order within a level
            levels.append([by_id[nid] for nid in ready])
            done |= set(ready)
        return levels


def load_arches(directory: str | Path = ARCHES_DIR) -> list[Arch]:
    arches = []
    for p in sorted(Path(directory).glob("*.yaml")):
        arches.append(Arch.from_dict(yaml.safe_load(p.read_text(encoding="utf-8"))))
    return arches


def load_lenses(path: str | Path = LENSES_PATH) -> dict[str, str]:
    if not Path(path).exists():
        return {}
    return {k: " ".join(str(v).split()) for k, v in yaml.safe_load(Path(path).read_text(encoding="utf-8")).items()}
