.PHONY: run test test-unit test-integration test-all lab-setup

## run: Start the FastAPI app on http://127.0.0.1:8000
run:
	python -m backend.main

## test: Run fast unit tests (FakeLLMClient — no network, no credentials)
test: test-unit

test-unit:
	pytest -q

## test-integration: real_llm-marked tests against the actual `opencode` CLI.
##   Requires `opencode` on the PATH with a configured provider.
test-integration:
	LEARN_GREEK_REAL_LLM=1 pytest -q -m real_llm

## test-all: every layer available in the current environment
test-all: test-unit test-integration

## lab-setup: one-time storylab setup — spaCy Greek model, frequency list, lemma cache
lab-setup:
	cd storylab && just setup
