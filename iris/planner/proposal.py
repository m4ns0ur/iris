import json
from typing import Any

from jinja2 import Environment, FileSystemLoader, StrictUndefined

from iris.llm.client import LLMClient
from iris.planner.models.proposal import Proposal
from iris.utils.json_utils import extract_json


class ProposalBuilder:
    def __init__(self) -> None:
        self.llm = LLMClient()
        self.env = Environment(
            loader=FileSystemLoader("iris/prompts"),
            undefined=StrictUndefined,  # fail if variable missing
            trim_blocks=True,
            lstrip_blocks=True,
            keep_trailing_newline=True,
        )

    def build(
        self,
        intent: str,
        clarifications: dict[str, Any],
    ) -> Proposal:
        template = self.env.get_template("proposal.md")

        clarifications_str = (
            json.dumps(clarifications, indent=2) if clarifications else "None"
        )

        prompt = template.render(intent=intent, clarifications=clarifications_str)

        raw_output = self.llm.generate(
            prompt=prompt,
            temperature=0.15,
        )

        data = extract_json(raw_output)
        return Proposal.model_validate(data)


def render_markdown(proposal: Proposal) -> str:
    md = f"""
# Iris Project Proposal

## Goal
{proposal.goal_summary}

## Architecture
- Language: {proposal.architecture.language}
- Runtime: {", ".join(proposal.architecture.runtime)}
- Framework: {", ".join(proposal.architecture.framework)}
- Entrypoint: {proposal.architecture.entrypoint}

## Core Features
"""
    for f in proposal.scope.core_features:
        md += f"- {f}\n"

    md += "\n## Optional Features\n"
    for f in proposal.scope.optional_features:
        md += f"- {f}\n"

    md += "\n## Explicitly Excluded\n"
    for f in proposal.scope.explicitly_excluded:
        md += f"- {f}\n"

    if proposal.uncertainties:
        md += "\n## Uncertainties\n"
        for u in proposal.uncertainties:
            md += f"- {u}\n"

    proposal.implementation_steps.sort(key=lambda s: s.step)
    md += "\n## Implementation Steps\n"
    for step in proposal.implementation_steps:
        md += f"{step.step}. {step.description}\n"
        md += f"  - Produces: {', '.join(step.produces)}\n"

    return md
