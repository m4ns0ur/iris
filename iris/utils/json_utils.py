import json
import re


def extract_json(text: str) -> dict:
    # remove markdown fences if present
    text = re.sub(r"```.*?```", "", text, flags=re.DOTALL)

    # find first { and last }
    start = text.find("{")
    end = text.rfind("}")

    if start == -1 or end == -1:
        raise ValueError("No JSON found")

    json_str = text[start : end + 1]
    return json.loads(json_str)
