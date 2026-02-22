import json
from pathlib import Path

from rich import print
import typer

from iris.executor.executor import Executor
from iris.executor.subtask_creator import SubtaskCreator
from iris.llm.client import LLMClient
from iris.planner.clarification import ClarificationBuilder
from iris.planner.classifier import Classifier
from iris.planner.proposal import ProposalBuilder
from iris.planner.renderer import render_markdown
from iris.tools.filesystem import FileSystemTool
from iris.tools.registry import ToolRegistry
from iris.tools.shell import ShellTool
from iris.utils.logger import logger, set_debug

app = typer.Typer(invoke_without_command=True)


# Make `plan` the default command if no subcommand is specified
@app.callback(invoke_without_command=True)
def main(
    ctx: typer.Context,
    intent: str = typer.Argument(None),
    debug: bool = typer.Option(False, "--debug", help="Enable debug logs"),
):
    set_debug(debug)

    if intent is not None:
        ctx.invoke(plan, intent=intent)


def build_executor(max_steps: int = 5) -> Executor:
    """
    Centralized executor construction.
    Keeps Iris immutable.
    """
    llm = LLMClient()
    registry = ToolRegistry()
    registry.register("filesystem", FileSystemTool(root=str(Path.cwd())))
    registry.register("shell", ShellTool())

    logger.debug("Executor tools registered: filesystem, shell")

    return Executor(
        llm=llm,
        tools=registry,
        max_steps=max_steps,
    )


@app.command()
def plan(intent: str):
    """
    Main Iris planning/execution command.
    """
    logger.debug(f"Starting plan for intent: {intent}")

    classifier = Classifier()
    clarification_builder = ClarificationBuilder()
    proposal_builder = ProposalBuilder()

    classification = classifier.build(intent)
    logger.debug(f"Task classification: {classification.task_type}")

    # -----------------------------
    # Atomic / Script → Direct Execution via Executor
    # -----------------------------
    if classification.task_type in ("atomic", "script"):
        print("[bold cyan]Iris detected an executable task.[/bold cyan]")
        print(f"[dim]{classification.reasoning}[/dim]\n")

        executor = build_executor()
        result = executor.run(intent)
        logger.debug(f"Executor result: {result}")

        print("\n[bold green]Execution Result:[/bold green]\n")
        print(result)
        return

    # -----------------------------
    # Architectural → Planning Flow
    # -----------------------------
    print("[bold cyan]Iris detected an architectural task.[/bold cyan]")
    print(f"[dim]{classification.reasoning}[/dim]\n")

    # ---- Phase 1: Clarification ----
    clarification_set = clarification_builder.build(intent)
    logger.debug(f"Clarifications generated: {clarification_set}")

    answers = {}
    defaults_used = 0

    if clarification_set.questions:
        print("\n[bold]Iris needs clarification:[/bold]\n")

        for i, q in enumerate(clarification_set.questions, 1):
            print(f"{i}. {q.question}")
            print(f"   [dim]Recommended: {q.recommended_default}[/dim]")
            user_input = input("   > ").strip()

            if user_input == "":
                answers[q.question] = q.recommended_default
                defaults_used += 1
            else:
                answers[q.question] = user_input

            logger.debug(f"Answer recorded: {q.question} → {answers[q.question]}")
            print()

    # ---- Phase 2: Proposal ----
    proposal = proposal_builder.build(
        intent=intent,
        clarifications=answers,
        task_type=classification.task_type,
        defaults_used=defaults_used,
    )
    logger.debug(f"Proposal generated: {proposal.model_dump()}")

    md = render_markdown(proposal)
    print(md)

    # ---- Phase 3: Execution Planning ----
    subtask_creator = SubtaskCreator()
    execution_plan = subtask_creator.build(proposal)
    logger.debug(f"Execution plan: {execution_plan.model_dump()}")

    print("\n[bold]Execution Plan:[/bold]\n")
    print(json.dumps(execution_plan.model_dump(), indent=2))

    print("\n[bold cyan]Executing subtasks...[/bold cyan]\n")

    executor = build_executor(max_steps=10)

    for i, task in enumerate(execution_plan.subtasks, 1):
        print(f"[bold]Step {i}:[/bold] {task.description}")
        logger.debug(f"Executing subtask: {task}")

        result = executor.run(task.description, shared_context=proposal)

        logger.debug(f"Subtask result: {result}")

        print(f"[green]Result:[/green]\n{result}\n")

        if "Execution stopped" in result:
            print("[red]Subtask failed. Halting project execution.[/red]")
            break


if __name__ == "__main__":
    app()
