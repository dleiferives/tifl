from backend.core.coverage import coverage, tokenize


def test_tokenize_strips_punctuation_and_casefolds():
    assert tokenize("Καλημέρα, Νίκο!") == ["καλημέρα", "νίκο"]


def test_coverage_full_match():
    text = "ο σκύλος τρώει"
    known = ["ο", "σκύλος", "τρώει"]
    assert coverage(text, known) == 1.0


def test_coverage_partial():
    text = "ο σκύλος τρώει το φαγητό"
    known = ["ο", "σκύλος", "τρώει"]
    assert abs(coverage(text, known) - 0.6) < 1e-9


def test_coverage_empty_text_is_perfect():
    assert coverage("", ["ο"]) == 1.0


def test_coverage_multiword_chunk_expands():
    text = "καλή μέρα φίλε"
    known = ["καλή μέρα", "φίλε"]
    assert coverage(text, known) == 1.0
