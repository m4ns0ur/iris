from pydantic import BaseModel


class Classification(BaseModel):
    task_type: str  # "atomic" | "script" | "project"
    reasoning: str
