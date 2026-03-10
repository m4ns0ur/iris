import json
import re
from typing import Any


def extract_json(text: str) -> Any:
    text = text.strip()

    # fast path
    if text.startswith("{") and text.endswith("}"):
        return json.loads(text)

    # remove markdown fences but keep content
    text = re.sub(r"```(?:json)?", "", text)
    text = text.replace("```", "")

    start = text.find("{")
    if start == -1:
        raise ValueError("No JSON found")

    decoder = json.JSONDecoder()
    obj, _ = decoder.raw_decode(text[start:])
    return obj
