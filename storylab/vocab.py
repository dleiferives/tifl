"""Vocabulary profiles + Greek lemmatization — how "at-level" is measured.

Coverage-against-an-explicit-list only works in the constrained regime (a tiny
beginner word bank). At higher levels the known set is huge, so list membership
saturates to ~100% and stops discriminating. A VocabularyProfile generalizes the
known set:

  explicit   — a literal word list (ideal for cold-start / controlled beginner)
  frequency  — the top-N lemmas of a corpus frequency list, N per level
               (scales to any level with no hand-authoring)

Everything is measured on LEMMAS (via spaCy `el_core_news_sm`), so γάτα / γάτες /
γάτας count as one word. If spaCy or the model is unavailable we fall back to
surface tokens, so the harness still runs (and tests stay fast/offline).

Metrics a profile exposes — all usable in arch `when:`/`until:` conditions:
  coverage(text)    fraction of lemmas inside the profile (in-band rate)
  oov_rate(text)    1 - coverage; the informative tail at high levels
  mean_rarity(text) mean log10 frequency-rank of lemmas; a direct difficulty proxy
"""
from __future__ import annotations

import functools
import json
import math
from pathlib import Path
from typing import Iterable

from backend.core.coverage import tokenize as _surface_tokens

VOCAB_DIR = Path(__file__).resolve().parent / "vocab"
FREQ_FILE = VOCAB_DIR / "el_50k.txt"
LEMMA_RANK_FILE = VOCAB_DIR / "el_lemma_rank.json"

# Top-N lemmas treated as "known" for a frequency profile at each level.
LEVEL_BANDS: dict[str, int] = {
    "absolute_beginner": 800,
    "intermediate": 3000,
    "advanced": 8000,
}
DEFAULT_BAND = 3000
_OOV_RANK = 10 ** 7  # rarity penalty for a lemma not in the frequency list


# ---- lemmatizer: lazy spaCy singleton, surface fallback -------------------
_nlp = None
_nlp_tried = False


def _get_nlp():
    global _nlp, _nlp_tried
    if _nlp_tried:
        return _nlp
    _nlp_tried = True
    try:
        import spacy
        _nlp = spacy.load("el_core_news_sm", disable=["parser", "ner"])
    except Exception:
        _nlp = None
    return _nlp


def lemmas_available() -> bool:
    return _get_nlp() is not None


@functools.lru_cache(maxsize=8192)
def lemmatize(text: str) -> tuple[str, ...]:
    nlp = _get_nlp()
    if nlp is None:
        return tuple(_surface_tokens(text))
    out: list[str] = []
    for tok in nlp(text):
        if tok.is_space or tok.is_punct:
            continue
        lem = (tok.lemma_ or tok.text).strip().casefold()
        if lem:
            out.append(lem)
    return tuple(out)


def lemma_set(chunks: Iterable[str]) -> set[str]:
    s: set[str] = set()
    for c in chunks:
        if c:
            s |= set(lemmatize(c))
    return s


# ---- frequency ranks (lemma -> 1-based rank) ------------------------------
_ranks: dict[str, int] | None = None


def _build_lemma_rank() -> dict[str, int]:
    """Lemmatize the surface frequency list into lemma -> best (lowest) rank."""
    words = [ln.split(" ", 1)[0] for ln in FREQ_FILE.read_text(encoding="utf-8").splitlines() if ln.strip()]
    ranks: dict[str, int] = {}
    nlp = _get_nlp()
    if nlp is None:  # surface fallback: no caching to disk (mode-dependent)
        for i, w in enumerate(words, start=1):
            ranks.setdefault(w.casefold(), i)
        return ranks
    for i, doc in enumerate(nlp.pipe(words, batch_size=2000), start=1):
        for tok in doc:
            lem = (tok.lemma_ or tok.text).strip().casefold()
            if lem:
                ranks.setdefault(lem, i)
    LEMMA_RANK_FILE.write_text(json.dumps(ranks, ensure_ascii=False), encoding="utf-8")
    return ranks


def ranks() -> dict[str, int]:
    global _ranks
    if _ranks is None:
        # The disk cache is lemma-keyed (built by spaCy); only use it when spaCy
        # is the active tokenizer, else its keys won't match surface tokens.
        if lemmas_available() and LEMMA_RANK_FILE.exists():
            _ranks = {k: int(v) for k, v in json.loads(LEMMA_RANK_FILE.read_text(encoding="utf-8")).items()}
        else:
            _ranks = _build_lemma_rank()
    return _ranks


# ---- the profile -----------------------------------------------------------
class VocabularyProfile:
    """A known-word set: an explicit lemma set, a frequency band, or both."""

    def __init__(self, known: set[str] | None = None, band: int | None = None) -> None:
        self.known = known        # explicit / pinned lemmas
        self.band = band          # top-N frequency band, or None

    def contains(self, lemma: str) -> bool:
        if self.known and lemma in self.known:
            return True
        if self.band is not None:
            r = ranks().get(lemma)
            return r is not None and r <= self.band
        return False

    def coverage(self, text: str) -> float:
        lems = lemmatize(text)
        if not lems:
            return 1.0
        return round(sum(1 for l in lems if self.contains(l)) / len(lems), 4)

    def oov_rate(self, text: str) -> float:
        return round(1.0 - self.coverage(text), 4)

    def mean_rarity(self, text: str) -> float:
        lems = lemmatize(text)
        if not lems:
            return 0.0
        r = ranks()
        return round(sum(math.log10(r.get(l, _OOV_RANK)) for l in lems) / len(lems), 3)


def build_profile(spec, level, extra_chunks: Iterable[str] = ()) -> VocabularyProfile:
    """Resolve a spec's vocabulary profile.

    `spec.vocab` may be {"kind": "explicit"} or {"kind": "frequency", "top_n": N}.
    Default: explicit if the spec lists available_chunks, else a frequency band
    sized by the level. Planner-introduced `extra_chunks` always count as known.
    """
    extra = lemma_set(extra_chunks)
    vocab = getattr(spec, "vocab", None) or {}
    kind = vocab.get("kind") or ("explicit" if spec.available_chunks else "frequency")

    if kind == "explicit":
        return VocabularyProfile(known=lemma_set(spec.available_chunks) | extra, band=None)

    top_n = int(vocab.get("top_n") or LEVEL_BANDS.get(spec.level_id, DEFAULT_BAND))
    # available_chunks on a frequency seed become pinned-known on top of the band.
    return VocabularyProfile(known=(lemma_set(spec.available_chunks) | extra) or None, band=top_n)
