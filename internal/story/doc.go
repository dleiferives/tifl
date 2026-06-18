// Package story owns the story generation pipeline: the discrete, individually
// checkpointed stages (selection -> story/phrase generation -> tokenization ->
// task generation) that every session type runs through. A failed stage is
// retried from that stage, not from the beginning. See context/session-types.md
// ("Generation Pipeline") and context/prompting-system.md.
package story
