from pydantic import BaseModel


class Todo(BaseModel):
    step: int
    description: str


class TodoList(BaseModel):
    todos: list[Todo]
