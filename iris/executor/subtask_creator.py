from pathlib import Path

from iris.executor.models.subtask_creator import ExecutionPlan
from iris.llm.client import LLMClient
from iris.planner.models.proposal import Proposal
from iris.utils.json_utils import extract_json


class SubtaskCreator:
    def __init__(self) -> None:
        self.llm = LLMClient()

    def build(self, proposal: Proposal) -> ExecutionPlan:
        """
        Generate ordered microtasks from a Proposal.
        Each microtask should be executable and produce a working intermediate program.
        """
        # Prepare input for LLM
        prompt_template = Path("iris/executor/prompts/subtask_creator.md").read_text()

        core_features = "\n".join(f"- {f}" for f in proposal.scope.core_features)

        prompt = (
            prompt_template.replace("[LANGUAGE]", proposal.architecture.language)
            .replace("[RUNTIME]", ", ".join(proposal.architecture.runtime))
            .replace("[FRAMEWORK]", proposal.architecture.framework)
            .replace("[PROJECT_STRUCTURE]", proposal.architecture.project_structure)
            .replace("[ENTRYPOINT]", proposal.architecture.entrypoint)
            .replace("[CORE_FEATURES]", core_features)
        )

        raw_output = self.llm.generate(prompt=prompt, temperature=0.2)

        data = extract_json(raw_output)
        return ExecutionPlan.model_validate(data)
