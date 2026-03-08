from jinja2 import Environment, FileSystemLoader, StrictUndefined

from iris.executor.models.subtask_creator import ExecutionPlan
from iris.llm.client import LLMClient
from iris.planner.models.proposal import Proposal
from iris.utils.json_utils import extract_json


class SubtaskCreator:
    def __init__(self) -> None:
        self.llm = LLMClient()
        self.env = Environment(
            loader=FileSystemLoader("iris/prompts"),
            undefined=StrictUndefined,  # fail if variable missing
            trim_blocks=True,
            lstrip_blocks=True,
            keep_trailing_newline=True,
        )

    def build(self, proposal: Proposal) -> ExecutionPlan:
        template = self.env.get_template("subtask_creator.md")

        core_features = list(proposal.scope.core_features)

        prompt = template.render(
            intent=proposal.goal_summary,
            language=proposal.architecture.language,
            runtime=proposal.architecture.runtime,
            framework=proposal.architecture.framework,
            entrypoint=proposal.architecture.entrypoint,
            core_features=core_features,
            implementation_steps=proposal.implementation_steps,
        )

        raw_output = self.llm.generate(prompt=prompt)

        data = extract_json(raw_output)
        return ExecutionPlan.model_validate(data)
