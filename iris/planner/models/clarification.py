from pydantic import BaseModel


class ClarificationQuestion(BaseModel):
    question: str
    recommended_default: str | None = None


class ClarificationSet(BaseModel):
    questions: list[ClarificationQuestion]
