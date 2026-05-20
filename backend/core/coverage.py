"""Compute the fraction of story words that come from the known chunk bank.

Greek tokenisation: split on whitespace, strip leading/trailing punctuation, casefold.
Known-words are matched whole-token against the chunk bank.
"""
from __future__ import annotations

import re
from typing import Iterable

_PUNCT_RE = re.compile(r"^[\W_]+|[\W_]+$", re.UNICODE)


def tokenize(text: str) -> list[str]:
    tokens: list[str] = []
    for raw in text.split():
        stripped = _PUNCT_RE.sub("", raw)
        if stripped:
            tokens.append(stripped.casefold())
    return tokens


def coverage(text: str, known_chunks: Iterable[str]) -> float:
    tokens = tokenize(text)
    if not tokens:
        return 1.0
    known_set = {c.casefold() for c in known_chunks if c}
    expanded: set[str] = set()
    for chunk in known_set:
        for tok in chunk.split():
            tok = _PUNCT_RE.sub("", tok)
            if tok:
                expanded.add(tok)
    hit = sum(1 for t in tokens if t in expanded)
    return hit / len(tokens)
