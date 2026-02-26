import json
from pathlib import Path
from typing import Any

from iris.llm.client import LLMClient
from iris.planner.models.proposal import Proposal
from iris.utils.enum_repair import EnumRepairEngine
from iris.utils.json_utils import extract_json


class ProposalBuilder:
    def __init__(self) -> None:
        self.llm = LLMClient()
        self.enum_repair = EnumRepairEngine()

        self.enum_repair.register_enum_field(
            field_path="architecture.runtime",
            allowed_values={"CLI", "GUI", "WEB", "LIBRARY"},
            is_list=True,
            redirect_invalid_to="architecture.framework",
        )

    def _calibrate_confidence(
        self,
        proposal: Proposal,
        defaults_used: int,
    ) -> float:
        base = proposal.confidence_score or 0.0

        penalty = 0.0

        # Penalize uncertainties
        penalty += min(0.05 * len(proposal.uncertainties), 0.3)

        # Penalize defaulted clarifications
        penalty += 0.03 * defaults_used

        # Penalize large plans
        if len(proposal.implementation_steps) > 6:
            penalty += 0.05

        adjusted = max(0.0, base - penalty)

        return round(adjusted, 2)

    def build(
        self,
        intent: str,
        clarifications: dict[str, Any],
        task_type: str,
        defaults_used: int = 0,
    ) -> Proposal:
        prompt_template = Path("iris/planner/prompts/proposal.md").read_text()

        prompt = (
            prompt_template.replace("[TASK_TYPE]", task_type)
            .replace("[INTENT]", intent)
            .replace("[CLARIFICATIONS]", json.dumps(clarifications, indent=2))
        )

        raw_output = self.llm.generate(
            prompt=prompt,
            temperature=0.15,
        )

        data = extract_json(raw_output)
        data = self.enum_repair.repair(data)
        proposal = Proposal.model_validate(data)
        defaults_used = sum(1 for q in proposal.uncertainties)
        proposal.confidence_score = self._calibrate_confidence(proposal, defaults_used)

        return proposal
