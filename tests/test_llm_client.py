from backend.llm.client import FakeLLMClient, LLMError, extract_json


def test_extract_json_plain():
    assert extract_json('{"a": 1}') == {"a": 1}


def test_extract_json_fenced():
    assert extract_json('```json\n{"a": 2}\n```') == {"a": 2}


def test_extract_json_with_prose():
    text = "Sure thing, here you go:\n{\"a\": 3}\nthanks!"
    assert extract_json(text) == {"a": 3}


def test_extract_json_invalid_returns_none():
    assert extract_json("nope") is None


def test_fake_client_returns_scripted():
    fake = FakeLLMClient([{"x": 1}, "raw string"])
    a = fake.call("p1", kind="k")
    assert a.parsed_json == {"x": 1}
    b = fake.call("p2", kind="k", expect_json=False)
    assert b.result_text == "raw string"


def test_fake_client_runs_out():
    fake = FakeLLMClient([])
    try:
        fake.call("p", kind="k")
    except LLMError:
        pass
    else:
        raise AssertionError("expected LLMError")
