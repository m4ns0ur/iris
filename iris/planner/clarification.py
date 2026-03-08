from jinja2 import Environment, FileSystemLoader, StrictUndefined

from iris.llm.client import LLMClient
from iris.planner.models.clarification import ClarificationSet
from iris.utils.json_utils import extract_json


class ClarificationBuilder:
    def __init__(self) -> None:
        self.llm = LLMClient()
        self.env = Environment(
            loader=FileSystemLoader("iris/prompts"),
            undefined=StrictUndefined,  # fail if variable missing
            trim_blocks=True,
            lstrip_blocks=True,
            keep_trailing_newline=True,
        )

    def build(self, intent: str) -> ClarificationSet:
        template = self.env.get_template("clarification.md")

        prompt = template.render(intent=intent)

        raw_output = self.llm.generate(
            prompt=prompt,
            temperature=0.2,
        )

        data = extract_json(raw_output)
        return ClarificationSet.model_validate(data)
