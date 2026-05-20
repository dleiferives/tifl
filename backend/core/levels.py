"""Difficulty levels + task type catalogue.

Levels carry the story-generation rules (verbatim prompt fragments) plus
defaults for max-new-chunks and task selection. Task types carry an id, a
human label, level affinity, and the schema the LLM is asked to produce.

Everything here is data — no behaviour. The pipeline + frontend both read it.
"""
from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any


@dataclass(frozen=True)
class Level:
    id: str
    label: str
    description: str
    story_rules: str
    default_task_types: tuple[str, ...]
    max_new_chunks: int
    coverage_target: float = 0.95


# ---- Absolute Beginner ----------------------------------------------------
# Uses the user-supplied beginner-reader rules verbatim. Heavy repetition,
# tiny vocab, full sentences only. No grammar metalanguage in output.
BEGINNER_RULES = """\
You are writing a beginner Modern Greek reader. Write the story in Greek only.

Vocabulary rules:
- Use only the most common, high-frequency Greek words possible.
- Introduce as few new words as necessary — recycle and repeat constantly.
- When you need to express something new, find a way to say it using words
  already established in the story.

Repetition rules:
- Repeat and restate constantly, building on each sentence. Example:
  "Το δέντρο είναι μεγάλο. Το δέντρο είναι πολύ μεγάλο και πράσινο.
   Μου αρέσει πολύ αυτό το δέντρο."
- When a new noun appears, spend several sentences describing it using only
  adjectives already in the story.
- When the scene moves, re-establish the setting before anything happens.

Sentence rules:
- Every sentence must be complete and grammatically correct. No fragments,
  no noun phrases standing alone.

Naturalness rules:
- Natural, contemporary Modern Greek. Avoid stiff word-for-word translations
  from English. Use natural Greek phrasing even if slightly more complex.
Rules for vocabulary:

Use only the most common, high-frequency Greek words possible
Introduce as few new words as necessary — the goal is to recycle and repeat the same small set of words constantly
When you want to express something new, find a way to say it using words already established in the story

Rules for repetition:

Repeat and restate constantly, building on each sentence. "Το δέντρο είναι μεγάλο. Το δέντρο είναι πολύ μεγάλο και πράσινο. Μου αρέσει πολύ αυτό το δέντρο." This is intentional and good.
When a new noun appears, spend several sentences describing it using only adjectives already introduced in the story
When the character moves to a new place, re-establish the setting with several descriptive sentences before anything happens there

Rules for sentences:

Every single sentence must be complete and grammatically correct — no fragments, no partial sentences, no noun phrases standing alone
Bad: "Μεγάλα, πράσινα δέντρα." Good: "Τα δέντρα είναι μεγάλα και πράσινα."
Bad: "Καλός φίλος." Good: "Ο Τομ είναι πολύ καλός φίλος μου."

Rules for naturalness:

Write in natural, contemporary Modern Greek — the kind a native speaker would actually say or write
Avoid constructions that are technically correct but sound like word-for-word translations from English — if it sounds stiff or foreign, rewrite it
Use natural Greek expressions: "πάω σπίτι" not "πηγαίνω στο σπίτι μου", "κάνει ζέστη" not "είμαι ζεστά", "βραδιάζει" not "ο ήλιος πηγαίνει κάτω"
Prefer the natural Greek phrasing even if it is slightly more complex than a direct translation

Format rules:
- Greek only. No English anywhere, not even in a title.
- Write numbers as full Greek words.
- One continuous piece with a short Greek title.
- No translation, no glossary, no explanations.
"""

# ---- Intermediate ---------------------------------------------------------
INTERMEDIATE_RULES = """\
You are writing a short Modern Greek text for an intermediate learner. Greek only.

Vocabulary:
- Recycle high-frequency words but introduce 2-4 new chunks where natural.
- Vary phrasing more than at the beginner level — synonyms are welcome when
  the meaning is supported by context.

Sentence and discourse:
- Vary sentence length and structure. Mix simple and compound sentences.
- Use connectives (γιατί, όμως, ενώ, αν και) where they aid comprehension.
- Include at least one instance of the target grammatical construction so a
  learner can notice it in context.

Naturalness:
- Natural, contemporary Modern Greek. Avoid translationese.

Format:
- Greek only, including any title. No English, no glossary, no explanations.
"""

# ---- Advanced -------------------------------------------------------------
ADVANCED_RULES = """\
You are writing a short Modern Greek text for an advanced learner. Greek only.

Vocabulary and register:
- Use a varied, natural Modern Greek lexicon. Idiomatic expressions are fine.
- Introduce nuanced or lower-frequency words when they fit; the learner has
  context and a glossary to fall back on.

Discourse:
- Coherent prose with natural transitions. Engage the target construction in
  more than one position and form.
- Subordination, varied tenses, and stylistic variation are encouraged.

Naturalness:
- Idiomatic Modern Greek. No translationese. No metalanguage.

Format:
- Greek only. No English. No glossary. No explanations.
"""

LEVELS: dict[str, Level] = {
    "absolute_beginner": Level(
        id="absolute_beginner",
        label="Absolute Beginner",
        description="Tiny vocab, heavy repetition, full simple sentences. Designed for cold-start with no vocabulary.",
        story_rules=BEGINNER_RULES,
        default_task_types=("yes_no", "comprehension_basic"),
        max_new_chunks=5,
        coverage_target=0.10,
    ),
    "intermediate": Level(
        id="intermediate",
        label="Intermediate",
        description="Varied phrasing, longer sentences, target one grammatical construction in context.",
        story_rules=INTERMEDIATE_RULES,
        default_task_types=("comprehension_open", "fill_blank", "transformation"),
        max_new_chunks=4,
        coverage_target=0.85,
    ),
    "advanced": Level(
        id="advanced",
        label="Advanced",
        description="Natural idiomatic prose. Multiple construction uses. Production-heavy tasks.",
        story_rules=ADVANCED_RULES,
        default_task_types=("comprehension_open", "reconstruction", "free_response"),
        max_new_chunks=6,
        coverage_target=0.95,
    ),
}

DEFAULT_LEVEL = "absolute_beginner"


def get_level(level_id: str | None) -> Level:
    return LEVELS.get(level_id or DEFAULT_LEVEL, LEVELS[DEFAULT_LEVEL])


# ---- Task catalogue -------------------------------------------------------
# Each entry describes how the task is presented to the LLM (instruction) and
# the minimal output schema. The pipeline picks these up at run-time. To add
# a new task type: append an entry here, optionally update level defaults.

@dataclass(frozen=True)
class TaskType:
    id: str
    label: str
    description: str
    difficulty: str  # "easy" | "medium" | "hard"
    instruction: str  # what the model is asked to produce
    schema: str  # JSON schema example string


TASK_TYPES: tuple[TaskType, ...] = (
    TaskType(
        id="yes_no",
        label="Yes/No question",
        description="A single Greek question with a yes/no answer.",
        difficulty="easy",
        instruction=(
            "Produce ONE yes/no question in Greek about the story. The answer must be "
            "unambiguous from the story. Provide the expected answer as 'ναι' or 'όχι'."
        ),
        schema='{"question_greek":"...","expected_answer":"ναι"}',
    ),
    TaskType(
        id="multiple_choice",
        label="Multiple choice",
        description="A Greek question with 3 short Greek options.",
        difficulty="easy",
        instruction=(
            "Produce ONE multiple-choice question in Greek about the story. Provide 3 short "
            "Greek choices and the index (0-based) of the correct one."
        ),
        schema='{"question_greek":"...","choices_greek":["...","...","..."],"answer_index":0}',
    ),
    TaskType(
        id="fill_blank",
        label="Fill in the blank",
        description="A single sentence from the story with one word blanked out.",
        difficulty="easy",
        instruction=(
            "Pick ONE sentence from the story and replace exactly one key word with '___'. "
            "Provide the correct word for the blank."
        ),
        schema='{"sentence_with_blank_greek":"Ο σκύλος ___ νερό.","answer_greek":"πίνει"}',
    ),
    TaskType(
        id="comprehension_basic",
        label="Basic comprehension",
        description="One short Greek question; learner answers in 1-2 Greek words.",
        difficulty="easy",
        instruction=(
            "Produce ONE simple Greek comprehension question that can be answered with 1-2 "
            "Greek words drawn from the story. Provide acceptable short Greek answers."
        ),
        schema='{"question_greek":"...","acceptable_answers_greek":["...","..."]}',
    ),
    TaskType(
        id="comprehension_open",
        label="Open comprehension",
        description="One open Greek question; learner answers in a full sentence.",
        difficulty="medium",
        instruction=(
            "Produce ONE open-ended Greek comprehension question requiring a full-sentence "
            "answer. Provide a single Greek model answer for evaluation."
        ),
        schema='{"question_greek":"...","model_answer_greek":"..."}',
    ),
    TaskType(
        id="reconstruction",
        label="Reconstruction",
        description="Retell a short passage from the story in own words.",
        difficulty="hard",
        instruction=(
            "Quote ONE short passage from the story and ask the learner (in Greek) to retell "
            "it in their own words. Keep instructions in Greek and concise."
        ),
        schema='{"instructions_greek":"...","passage_greek":"..."}',
    ),
    TaskType(
        id="transformation",
        label="Transformation",
        description="Rewrite a sentence with a small grammatical change.",
        difficulty="hard",
        instruction=(
            "Pick ONE sentence from the story and ask the learner (in Greek) to rewrite it "
            "applying ONE small transformation (e.g. change person, tense, or singular→plural). "
            "Provide a model transformed sentence."
        ),
        schema='{"instructions_greek":"...","source_sentence_greek":"...","model_transformed_greek":"..."}',
    ),
    TaskType(
        id="prediction",
        label="Prediction",
        description="Predict what could happen next.",
        difficulty="medium",
        instruction=(
            "Ask the learner (in Greek) to predict what happens next, in 1-2 Greek sentences."
        ),
        schema='{"prompt_greek":"..."}',
    ),
    TaskType(
        id="free_response",
        label="Free response",
        description="Give an opinion or related personal sentence in Greek.",
        difficulty="medium",
        instruction=(
            "Ask the learner (in Greek) for a short personal response or opinion about the story."
        ),
        schema='{"prompt_greek":"..."}',
    ),
)


def get_task_type(task_id: str) -> TaskType | None:
    return next((t for t in TASK_TYPES if t.id == task_id), None)
