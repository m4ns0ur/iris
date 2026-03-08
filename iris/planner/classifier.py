from jinja2 import Environment, FileSystemLoader, StrictUndefined

from iris.llm.client import LLMClient
from iris.planner.models.classifier import Classification
from iris.utils.json_utils import extract_json


class Classifier:
    def __init__(self, model: str | None = None):
        self.llm = LLMClient()
        self.env = Environment(
            loader=FileSystemLoader("iris/prompts"),
            undefined=StrictUndefined,  # fail if variable missing
            trim_blocks=True,
            lstrip_blocks=True,
            keep_trailing_newline=True,
        )

    def build(self, intent: str) -> Classification:
        template = self.env.get_template("classifier.md")
        prompt = template.render(intent=intent)

        raw_output = self.llm.generate(
            prompt=prompt,
            temperature=0.1,
        )

        data = extract_json(raw_output)
        return Classification.model_validate(data)
