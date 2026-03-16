from pydantic import BaseModel

from iris.todo_maker.models.todo_maker import TodoList


class TodoCritique(BaseModel):
    valid: bool
    fix: TodoList | None = None
