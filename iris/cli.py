import json
from pathlib import Path

from rich import print
import typer

from iris.executor.executor import Executor
from iris.executor.subtask_creator import SubtaskCreator
from iris.llm.client import LLMClient
from iris.planner.clarification import ClarificationBuilder
from iris.planner.classifier import Classifier
from iris.planner.proposal import ProposalBuilder, render_markdown
from iris.tools.code import CodeTool
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
    workspace: Path = typer.Option(
        None,
        "--workspace",
        "-w",
        help="Workspace directory for project execution",
        exists=True,
        file_okay=False,
        dir_okay=True,
        resolve_path=True,
    ),
) -> None:
    set_debug(debug)

    # store in context for later usage
    ctx.obj = {"workspace": workspace or Path.cwd()}

    if intent is not None:
        plan(ctx, intent)


def build_executor(workspace: Path) -> Executor:
    llm = LLMClient()
    registry: ToolRegistry = ToolRegistry()

    registry.register("filesystem", FileSystemTool(root=str(workspace)))
    registry.register("code", CodeTool(root=str(workspace)))
    registry.register("shell", ShellTool(workdir=str(workspace)))

    logger.debug(f"Executor tools registered with workspace: {workspace}")

    return Executor(
        llm=llm,
        tools=registry,
    )


@app.command()
def plan(
    ctx: typer.Context,
    intent: str = typer.Argument(...),
) -> None:
    logger.debug(f"Starting plan for intent: {intent}")

    classifier = Classifier()
    classification = classifier.build(intent)
    logger.debug(f"Task classification: {classification.task_type}")

    workspace: Path = ctx.obj["workspace"]

    # -----------------------------
    # Atomic -> Direct Execution via Executor
    # -----------------------------
    if classification.task_type == "atomic":
        print("[bold cyan]Iris detected an executable task.[/bold cyan]")
        print(f"[dim]{classification.reasoning}[/dim]\n")

        executor = build_executor(workspace=workspace)
        result = executor.run(intent)
        logger.debug(f"Executor result: {result}")

        print("\n[bold green]Execution Result:[/bold green]\n")
        print(result)
        return

    # -----------------------------
    # Multi-step/Complex -> Planning Workflow
    # -----------------------------
    print(
        "[bold cyan]Iris detected a multi-step/complex task. Initiating planning workflow...[/bold cyan]"
    )
    print(f"[dim]{classification.reasoning}[/dim]\n")

    answers = {}

    if classification.task_type == "complex":
        print(
            "[yellow]Note: Complex tasks may require multiple iterations of clarification and planning.[/yellow]\n"
        )

        # ---- Clarification ----
        clarification_builder = ClarificationBuilder()
        clarification_set = clarification_builder.build(intent)
        logger.debug(f"Clarifications generated: {clarification_set}")

        if clarification_set.questions:
            print("\n[bold]Iris needs clarification:[/bold]\n")

            for i, q in enumerate(clarification_set.questions, 1):
                print(f"{i}. {q.question}")
                print(f"   [dim]Recommended: {q.recommended_default}[/dim]")
                user_input = input("   > ").strip()

                if user_input == "":
                    answers[q.question] = q.recommended_default
                else:
                    answers[q.question] = user_input

    # ---- Proposal ----
    proposal_builder = ProposalBuilder()
    proposal = proposal_builder.build(
        intent=intent,
        clarifications=answers,
    )
    logger.debug(f"Proposal generated: {proposal.model_dump()}")

    md = render_markdown(proposal)
    print(md)

    # ---- Execution Planning ----
    subtask_creator = SubtaskCreator()
    execution_plan = subtask_creator.build(proposal)
    logger.debug(f"Execution plan: {execution_plan.model_dump()}")

    print("\n[bold]Execution Plan:[/bold]\n")
    print(json.dumps(execution_plan.model_dump(), indent=2))

    print("\n[bold cyan]Executing subtasks...[/bold cyan]\n")

    executor = build_executor(workspace=workspace)

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
