import json
from pathlib import Path

from rich import print
import typer

from iris.executor.loop import Executor
from iris.executor.subtask_creator import SubtaskCreator
from iris.llm.client import LLMClient
from iris.planner.clarification import ClarificationBuilder
from iris.planner.classifier import Classifier
from iris.planner.proposal import ProposalBuilder
from iris.planner.renderer import render_markdown
from iris.tools.filesystem import FileSystemTool
from iris.tools.registry import ToolRegistry
from iris.tools.shell import ShellTool

app = typer.Typer()


def build_executor() -> Executor:
    """
    Centralized executor construction.
    Keeps iris immutable.
    """
    llm = LLMClient()

    registry = ToolRegistry()
    registry.register("filesystem", FileSystemTool(root=str(Path.cwd())))
    registry.register("shell", ShellTool())

    return Executor(
        llm=llm,
        tools=registry,
        max_steps=5,
    )


@app.command()
def plan(intent: str):
    classifier = Classifier()
    clarification_builder = ClarificationBuilder()
    proposal_builder = ProposalBuilder()

    classification = classifier.build(intent)

    # -----------------------------
    # Atomic / Script → Direct Execution via Executor
    # -----------------------------
    if classification.task_type in ("atomic", "script"):
        print("[bold cyan]iris detected an executable task.[/bold cyan]")
        print(f"[dim]{classification.reasoning}[/dim]\n")

        executor = build_executor()
        result = executor.run(intent)

        print("\n[bold green]Execution Result:[/bold green]\n")
        print(result)
        return

    # -----------------------------
    # Architectural → Planning Flow
    # -----------------------------
    print("[bold cyan]iris detected an architectural task.[/bold cyan]")
    print(f"[dim]{classification.reasoning}[/dim]\n")

    # ---- Phase 1: Clarification ----
    clarification_set = clarification_builder.build(intent)

    answers = {}
    defaults_used = 0

    if clarification_set.questions:
        print("\n[bold]iris needs clarification:[/bold]\n")

        for i, q in enumerate(clarification_set.questions, 1):
            print(f"{i}. {q.question}")
            print(f"   [dim]Recommended: {q.recommended_default}[/dim]")
            user_input = input("   > ").strip()

            if user_input == "":
                answers[q.question] = q.recommended_default
                defaults_used += 1
            else:
                answers[q.question] = user_input

            print()

    # ---- Phase 2: Proposal ----
    proposal = proposal_builder.build(
        intent=intent,
        clarifications=answers,
        task_type=classification.task_type,
        defaults_used=defaults_used,
    )

    md = render_markdown(proposal)
    print(md)

    # ---- Phase 3: Execution Planning ----
    subtask_creator = SubtaskCreator()
    execution_plan = subtask_creator.build(proposal)

    print("\n[bold]Execution Plan:[/bold]\n")
    print(json.dumps(execution_plan.model_dump(), indent=2))


if __name__ == "__main__":
    app()
