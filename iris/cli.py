from pathlib import Path
import sys

from rich import print
import typer

from iris.executor.executor import Executor
from iris.todo_maker.todo_maker import TodoMaker
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

    logger.debug(f"Create TODO list for intent: {intent}")

    todo_maker = TodoMaker(workspace=workspace, intent=intent)
    todo_list = todo_maker.create(intent)
    logger.debug(f"TODO list generated: {todo_list}")

    executor = Executor(workspace=workspace, intent=intent)

    for i, todo in enumerate(todo_list.todos, 1):
        print(f"[bold]Step {i}:[/bold] {todo.description}")
        result = executor.run(todo.description)
        print(f"[green]Result:[/green]\n{result}\n")

        if "Execution stopped" in result:
            print("[red]Subtask failed. Halting project execution.[/red]")
            break


@app.command()
def plan(
    ctx: typer.Context,
    intent: str = typer.Argument(...),
) -> None:
    logger.debug(f"Starting plan for intent: {intent}")
    sys.exit("Planning phase is not implemented yet.")


if __name__ == "__main__":
    app()
