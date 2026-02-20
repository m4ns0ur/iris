import json

from rich import print
import typer

from iris.executor.subtask_creator import SubtaskCreator
from iris.planner.clarification import ClarificationBuilder
from iris.planner.classifier import Classifier
from iris.planner.proposal import ProposalBuilder
from iris.planner.renderer import render_markdown

app = typer.Typer()


@app.command()
def plan(intent: str):
    classifier = Classifier()
    clarification_builder = ClarificationBuilder()
    proposal_builder = ProposalBuilder()

    # ---- Phase 0: Intent Classification ----
    classification = classifier.build(intent)

    if classification.task_type == "atomic":
        print("[bold cyan]iris detected an atomic task.[/bold cyan]")
        print(f"[dim]{classification.reasoning}[/dim]\n")

        # For now, just print a minimal execution hint.
        # Later this can call an AtomicExecutor.
        print("[yellow]No architectural planning required.[/yellow]")
        print("You may directly execute this task.\n")
        return

    if classification.task_type == "script":
        print("[bold cyan]iris detected a small script task.[/bold cyan]")
        print(f"[dim]{classification.reasoning}[/dim]\n")
        # For now, treat scripts as lightweight projects.
        # Later you can create a dedicated ScriptPlanner.

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
    print(json.dumps(execution_plan.model_dump(), indent=2))


if __name__ == "__main__":
    app()
