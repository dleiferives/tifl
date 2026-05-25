"""Cheap, objective guardrail metrics for a story.

These do NOT measure "good story" — that needs judging. They measure the
*constraints* the pipeline is supposed to respect, so a variant that wins on
quality while quietly blowing past the vocabulary budget gets caught.
"""
from __future__ import annotations

import re
from typing import Any

from backend.core.coverage import coverage, tokenize

_SENT_SPLIT = re.compile(r"[.!?;·]+")


def sentences(text: str) -> list[str]:
    return [s.strip() for s in _SENT_SPLIT.split(text) if s.strip()]


def story_metrics(text: str, known_chunks: list[str]) -> dict[str, Any]:
    toks = tokenize(text)
    n_tokens = len(toks)
    n_types = len(set(toks))
    sents = sentences(text)
    n_sents = len(sents)
    sent_lens = [len(tokenize(s)) for s in sents]
    # most-repeated token share — a crude "is it circling or chanting" signal.
    top_share = 0.0
    if toks:
        from collections import Counter
        top_share = Counter(toks).most_common(1)[0][1] / n_tokens
    return {
        "n_tokens": n_tokens,
        "n_types": n_types,
        "type_token_ratio": round(n_types / n_tokens, 3) if n_tokens else 0.0,
        "n_sentences": n_sents,
        "avg_sentence_len": round(sum(sent_lens) / n_sents, 2) if n_sents else 0.0,
        # fraction of "sentences" that are 1 token — a fragment proxy.
        "frag_share": round(sum(1 for n in sent_lens if n <= 1) / n_sents, 3) if n_sents else 0.0,
        "top_token_share": round(top_share, 3),
        "coverage": round(coverage(text, known_chunks), 4),
    }
