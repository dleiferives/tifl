"""Pre-defined Greek construction taxonomy (from research)."""
from __future__ import annotations

import sqlite3

CONSTRUCTIONS_SEED: list[tuple[str, str]] = [
    ("nominative", "case"),
    ("genitive", "case"),
    ("accusative", "case"),
    ("vocative", "case"),
    ("aspect_perfective", "aspect"),
    ("aspect_imperfective", "aspect"),
    ("tense_present", "tense"),
    ("tense_aorist", "tense"),
    ("tense_imperfect", "tense"),
    ("tense_future", "tense"),
    ("person_1sg", "person_number"),
    ("person_2sg", "person_number"),
    ("person_3sg", "person_number"),
    ("person_1pl", "person_number"),
    ("person_2pl", "person_number"),
    ("person_3pl", "person_number"),
    ("mood_indicative", "mood"),
    ("mood_subjunctive", "mood"),
    ("mood_imperative", "mood"),
    ("gender_masculine", "gender_agreement"),
    ("gender_feminine", "gender_agreement"),
    ("gender_neuter", "gender_agreement"),
    ("article_definite", "article"),
    ("article_indefinite", "article"),
]


def seed_constructions(c: sqlite3.Connection) -> int:
    cur = c.execute("SELECT COUNT(*) FROM constructions")
    if cur.fetchone()[0] > 0:
        return 0
    c.executemany(
        "INSERT INTO constructions (construction_id, construction_type) VALUES (?, ?)",
        CONSTRUCTIONS_SEED,
    )
    c.commit()
    return len(CONSTRUCTIONS_SEED)
