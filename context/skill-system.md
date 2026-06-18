# Skill System

_Status: active design notes — designed during architecture expansion session_

## What Skills Are

Skills are a layer above knowledge items. Where `user_knowledge` tracks the user's
relationship with individual vocabulary items and constructions, skills track
broader linguistic competencies — things like "understanding the genitive case" or
"expressing time and sequence."

Skills are the primary driver of two things:
1. **Level progression** — accumulating skill tiers eventually promotes the user
   to the next level, which unlocks more complex story generation
2. **Story complexity control** — the prompt builder reads the user's current skill
   tiers and tells the LLM exactly which constructions and cases to use or avoid

Skills are language-specific. Greek has different skills than Arabic. The set for
each language is hard-coded in the language plugin. Count: roughly 60–100 per
language to start.

---

## XP Model

Skills use an XP (experience points) system. The user earns XP in a skill every
time they correctly complete a task that exercises that skill. They lose XP for
wrong answers once they reach higher tiers — this is the MMR-like aspect: the
system assumes higher-tier performance should be sustained, not just reached once.

### Tiers

Each skill has multiple tiers (e.g. three: Introduced / Practicing / Acquired).
XP thresholds define when the user crosses from one tier to the next. The
thresholds are tunable per skill and per language.

At each tier boundary, before promoting the user, the system calls an AI
verification pass (see below). Tiers do not advance automatically on XP alone.

### XP amounts by task type

Different task types reflect different levels of demonstrated knowledge and award
XP accordingly. These are configured per task type, not per skill — the task type
declares its base XP value, and the skill receives that amount when the task is
completed correctly.

Rough ordering by XP value (exact numbers are tunable):

| Task type | XP direction |
|-----------|-------------|
| Multiple choice comprehension | low positive |
| Fill-in-the-blank (controlled) | medium positive |
| Listening transcription | medium positive |
| Production / free writing | high positive |
| Wrong answer at tier 0 | no change |
| Wrong answer at tier 1+ | small negative |

The negative XP on wrong answers at higher tiers is intentional. It prevents a
user from reaching a high tier through volume of easy tasks and then failing to
demonstrate that level of understanding on harder ones. A user genuinely at tier 2
should be able to sustain performance there.

---

## Task → Skill Signal Flow

When a task is graded, the system maps the task's target knowledge items to their
associated skills, then awards or deducts XP accordingly.

The mapping is defined in the language plugin's skill definitions. Each skill
declares which knowledge item types or specific items it covers. This is
materialized at server startup into an `item_skill_associations` table so that
XP computation is a fast lookup, not a runtime classification call.

```
task graded
  └─ task_targets → knowledge item IDs
       └─ item_skill_associations → skill IDs for each item
            └─ task_type XP value × correct/incorrect → XP delta
                 └─ user_skill_xp updated per skill
```

A single task can award XP in multiple skills simultaneously if the targeted
knowledge items span multiple skills.

---

## AI Verification

XP alone does not promote a tier. When the user's XP crosses a tier threshold,
the system queues an AI verification pass for that skill.

The verifier receives:
- The skill definition (what this skill means, what it covers)
- A sample of the user's recent task responses involving this skill (last N correct
  and incorrect responses)
- The current tier they are being evaluated for

The verifier returns a binary judgment: does the demonstrated performance support
promotion to this tier? If yes, the tier is confirmed and XP continues accumulating
toward the next tier. If no, the XP is stepped back slightly below the threshold
(the user must accumulate more evidence before verification is attempted again).

The verification call is not on the critical path — it runs in the background after
the task grading response is returned to the client. The user sees their XP update
immediately; the tier promotion (if it occurs) is surfaced in a follow-up
notification or on the skill tree next time they view it.

---

## Level Promotion from Skills

"Level" in this system is a derived concept, not a directly managed field. When
enough skills reach sufficient tiers, the user is promoted to the next level. The
specific rules (which skills, at which tiers, in what proportions) are defined per
language in the language plugin.

Example rule structure (language-defined, not hard-coded in core):

```
Level: Elementary → Intermediate
  Required: 80% of "core grammar" skill category at tier 1+
            50% of "core vocabulary" skill category at tier 1+
            Any 3 skills in "intermediate constructions" category at tier 1+
```

Level promotion is computed by the hard system (no LLM). The computation runs
after each session completion and after each skill tier change.

---

## How Skills Drive Story Complexity

Rather than giving the LLM a vague "write at beginner level" instruction, the
prompt builder serializes the user's actual skill state into specific constraints:

```
[User skill state → Story generation constraint]
Nominative case: tier 2   → use nominative freely
Accusative case: tier 1   → include accusative in simple positions
Genitive case: tier 0     → avoid genitive except in memorized phrases
Genitive absolute: tier 0 → do not use
Aorist tense: tier 1      → introduce aorist in narrative past contexts
Perfect tense: tier 0     → avoid
```

The prompt builder translates the skill tier table into prose instructions and
structural constraints that the story generator must follow. This is more precise
than level labels, and it produces stories that are genuinely calibrated to what
the user can handle.

---

## Skill Tree Visualization

The user can see all skills for their active language. The view shows every skill,
its current tier (including tier 0 / not yet started), and an XP progress bar
toward the next tier.

Skills are organized into categories for display (e.g. "Cases", "Verb Forms",
"Constructions", "Vocabulary", "Pragmatics"). The user sees all categories and
all skills within them — not just the ones they've started. Seeing skills they
haven't touched yet provides a map of the language and motivates specific study.

The skill tree is read-only for the user. They cannot manually adjust skill
levels. The system's signals are the source of truth.

---

## Hard-Coded vs User-Defined Skills (Future)

The current design uses only hard-coded skills defined in the language plugin.
This is the right starting point: it keeps the system well-defined and avoids
the complexity of dynamic skill graphs.

**Future design note (not implemented):** User-defined skills allow the user to
specify a goal like "I want to be able to talk about art" and have the system
decompose that into sub-skills (artistic vocabulary, expressing opinions about
aesthetics, describing visual properties, etc.). Sub-skills can be shared between
user-defined skill trees — "expressing emotions about a thing" appears in both
"talking about art" and "talking about personal experiences." The XP earned in
shared sub-skills contributes to all parent skills simultaneously.

This design requires a skill dependency graph, a way to discover sub-skills from
a topic description (LLM-assisted), and an interface for managing user-defined
goals. It is deferred until the hard-coded skill system is proven.

---

## Database Tables

### `skills`

Hard-coded per language. Populated at server startup by language plugin
registration.

```
skills
  skill_id      TEXT  PK
  language      TEXT  NOT NULL    FK → languages.code
  name          TEXT  NOT NULL    e.g. "Genitive Case", "Aorist Tense"
  description   TEXT
  category      TEXT  NOT NULL    grouping for skill tree display
  tier_count    INT   NOT NULL    number of tiers (typically 3)
  xp_per_tier   INT   NOT NULL    XP threshold to reach each tier (same per tier;
                                  tuning per-tier is a future concern)
  sort_order    INT               display order within category
```

### `item_skill_associations`

Materialized at startup from language plugin skill definitions. Maps knowledge
items to the skills they count toward.

```
item_skill_associations
  item_id       TEXT  NOT NULL    FK → knowledge_items.item_id
  skill_id      TEXT  NOT NULL    FK → skills.skill_id

  PRIMARY KEY (item_id, skill_id)
```

### `user_skill_xp`

The user's current XP and tier per skill.

```
user_skill_xp
  user_id           TEXT  NOT NULL    FK → users.user_id
  skill_id          TEXT  NOT NULL    FK → skills.skill_id
  xp                INT   NOT NULL    DEFAULT 0
  tier              INT   NOT NULL    DEFAULT 0
  pending_verify    BOOL  NOT NULL    DEFAULT false   -- XP crossed threshold, awaiting AI
  last_verified_at  REAL              when AI last ran verification
  updated_at        REAL  NOT NULL

  PRIMARY KEY (user_id, skill_id)
```

### `task_skill_xp_log`

Every XP change, for auditability and for potential ML training later.

```
task_skill_xp_log
  log_id        TEXT  PK
  user_id       TEXT  NOT NULL
  task_id       TEXT  NOT NULL    FK → tasks.task_id
  skill_id      TEXT  NOT NULL    FK → skills.skill_id
  xp_delta      INT   NOT NULL    positive or negative
  xp_after      INT   NOT NULL    user's XP in this skill after this change
  logged_at     REAL  NOT NULL
```

---

## Open Questions

- Exact XP values per task type (need real data to calibrate)
- Exact XP-per-tier thresholds (same — empirical calibration needed)
- Whether tier regression (losing a tier, not just XP) should ever happen, or
  whether tiers are permanent once verified
- How to handle the case where a language plugin gains new skills after users have
  existing XP — new skills start at tier 0 for all users
