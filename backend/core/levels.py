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
# Grounded in comprehensible-input research (Krashen: input must be
# comprehensible AND compelling) and TPRS "circling" (recycle target vocabulary
# by carrying a small story forward, not by stacking flat descriptions). The
# failure mode we design against is the degenerate "Noun είναι adjective." list,
# which is comprehensible but not a story and not compelling.
BEGINNER_RULES = """\
You are writing a beginner Modern Greek reader: a COMPLETE little story an
absolute beginner can understand almost entirely from context. Greek only.

WHAT MAKES A GOOD STORY (this matters most):
- It is a STORY, not a description. A character (a person or an animal) WANTS
  something or has a small PROBLEM. They try. Something happens. By the end the
  problem is solved or changed, and the character FEELS something (χαρούμενος,
  λυπημένος, κουρασμένος, έκπληκτος).
- Keep it concrete and easy to picture: people, animals, objects, and simple
  actions in one or two places.
- Make it a little bit interesting or surprising — a tiny twist, a funny moment,
  or a strong feeling. Compelling input keeps the reader reading.

LENGTH — write a FULL reader, not a summary:
- Aim for roughly 20-30 short sentences across 3-5 short paragraphs. A few short
  sentences is too short; the learner needs lots of repeated exposure.
- The length comes from CIRCLING, not from padding: spend several sentences on
  each story beat — set it up, let the character act, ask a short question,
  answer it, and let the character react — all reusing the same small word set.
- Never cut the story down to its bare plot. Linger on each moment with the
  words the learner already has.

VOCABULARY (tiny, high-frequency set):
- Use the most common Greek words. Lean on high-frequency verbs of wanting,
  doing, and perceiving: θέλω, έχω, είμαι, πάω, βλέπω, ψάχνω, βρίσκω, παίρνω,
  δίνω, τρώω, λέω, κάνω, ξέρω.
- Introduce as few new words as possible and recycle them constantly.
- To say something new, build it from words already in the story.

REPETITION DONE RIGHT — "circling", not listing:
- Recycle a key word by carrying it through the ACTION: the character wants it,
  looks for it, finds it, uses it, reacts to it. The word repeats naturally
  because the plot keeps touching it.
  GOOD: "Η Άννα θέλει το μπλε χρώμα. Ψάχνει το μπλε χρώμα παντού. Πού είναι το
         μπλε χρώμα; Α, να το! Η Άννα παίρνει το μπλε χρώμα και είναι χαρούμενη."
- NEVER write long runs of "Noun + είναι + adjective." sentences. Do not write
  more than two description sentences in a row before something happens.
  BAD (forbidden): "Ο καμβάς είναι μεγάλος. Ο καμβάς είναι λευκός. Το πινέλο
                    είναι μικρό. Το χρώμα είναι μπλε."
- Reintroduce a noun through what the character DOES with it, not by listing
  its properties.

SENTENCES:
- Every sentence must be complete and grammatically correct. No fragments, no
  noun phrases standing alone.
  Bad: "Μεγάλα, πράσινα δέντρα." Good: "Τα δέντρα είναι μεγάλα και πράσινα."
- Vary the subject and the verb. Not every sentence should start the same way
  or use είναι. Mix actions, wants, and the occasional short question.

NATURALNESS:
- Natural, contemporary Modern Greek — what a native speaker would actually say.
- Avoid stiff word-for-word translations from English: "πάω σπίτι" not
  "πηγαίνω στο σπίτι μου"; "κάνει ζέστη" not "είμαι ζεστά".

FORMAT:
- Greek only. No English anywhere, not even in the title.
- Numbers as full Greek words.
- One continuous story with a short Greek title on the first line.
- No glossary, no explanations, no word lists.
"""

# ---- Intermediate ---------------------------------------------------------
INTERMEDIATE_RULES = """\
You are writing a short Modern Greek text for an intermediate learner: a small
but COMPLETE story with a clear shape. Greek only.

STORY SHAPE:
- A character with a goal or problem, a complication, and a resolution. Give it
  a little tension or surprise so it is worth reading.
- Coherent paragraphs that each move the story forward; no static description
  dumps.
- Length: a real short story, roughly 4-6 paragraphs (about 15-25 sentences).
  Develop each beat; do not compress the plot into a few lines.

Vocabulary:
- Recycle high-frequency words but introduce 2-4 new chunks where natural.
- Vary phrasing more than at the beginner level — synonyms are welcome when
  the meaning is supported by context.

Sentence and discourse:
- Vary sentence length and structure. Mix simple and compound sentences.
- Use connectives (γιατί, όμως, ενώ, αν και, έτσι) where they aid comprehension
  and carry the narrative.
- Weave the target grammatical construction in naturally, in more than one
  place, so the learner meets it several times in meaningful context.

Naturalness:
- Natural, contemporary Modern Greek. Avoid translationese.

Format:
- Greek only, including any title. No English, no glossary, no explanations.
"""

# ---- Advanced -------------------------------------------------------------
ADVANCED_RULES = """\
You are writing a short Modern Greek text for an advanced learner: a complete,
engaging piece with real narrative or rhetorical shape. Greek only.

Shape and content:
- A genuine arc: a situation that develops and resolves, or an idea explored and
  landed. Give it a point, a mood, or a perspective worth the reader's attention.
- Coherent prose with natural transitions and paragraphing.
- Length: a developed piece of several paragraphs (roughly 20-35 sentences), not
  a sketch. Give scenes and ideas room to breathe.

Vocabulary and register:
- Use a varied, natural Modern Greek lexicon. Idiomatic expressions are fine.
- Introduce nuanced or lower-frequency words when they fit; the learner has
  context and a glossary to fall back on.

Discourse:
- Engage the target construction in more than one position and form.
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
