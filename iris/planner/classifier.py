from pathlib import Path

from iris.llm.client import LLMClient
from iris.planner.models.classifier import Classification
from iris.utils.json_utils import extract_json


class Classifier:
    def __init__(self, model: str | None = None):
        self.llm = LLMClient()

    def build(self, intent: str) -> Classification:
        prompt_template = Path("iris/planner/prompts/classifier.md").read_text()

        prompt = prompt_template.replace("[INTENT]", intent)

        raw_output = self.llm.generate(
            prompt=prompt,
            temperature=0.1,
        )

        data = extract_json(raw_output)
        return Classification.model_validate(data)
