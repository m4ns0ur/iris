from pydantic import BaseModel


class Classification(BaseModel):
    task_type: str  # "atomic" | "multi-step" | "complex"
    reasoning: str
