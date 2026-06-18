// Package reader owns the reader domain: server-side story tokenization
// (story_tokens), the knowledge-lookup surface the client renders, and the
// behavioural signals the reader produces — lookups (Space presses), knowledge
// ratings, sentence/word breakdowns. lookup_count is the single strongest
// acquisition signal in the system. See context/reader-mode.md.
package reader
