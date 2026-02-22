from iris.planner.models.proposal import Proposal


def render_markdown(proposal: Proposal) -> str:
    if proposal.architecture.project_structure == "single_file":
        title = "# Iris Script Plan"
    else:
        title = "# Iris Project Proposal"

    md = f"""
{title}

## Goal
{proposal.goal_summary}

## Architecture
- Language: {proposal.architecture.language}
- Runtime: {", ".join(proposal.architecture.runtime)}
- Framework: {proposal.architecture.framework}
- Structure: {proposal.architecture.project_structure}
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

    md += f"\nConfidence: {proposal.confidence_score}\n"

    if proposal.uncertainties:
        md += "\n## Uncertainties\n"
        for u in proposal.uncertainties:
            md += f"- {u}\n"

    return md
