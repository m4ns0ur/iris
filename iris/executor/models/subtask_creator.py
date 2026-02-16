from pydantic import BaseModel


class Subtask(BaseModel):
    step: int
    description: str
    file: str
    action: str  # "create" or "modify"
    produces: list[str]
    depends_on: list[int]


class ExecutionPlan(BaseModel):
    subtasks: list[Subtask]
