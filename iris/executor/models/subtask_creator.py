from pydantic import BaseModel


class Subtask(BaseModel):
    step: int
    description: str
    file: str
    action: str  # "create" or "modify"
    produces: list[str]


class ExecutionPlan(BaseModel):
    subtasks: list[Subtask]
