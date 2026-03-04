from typing import Literal

from pydantic import BaseModel, field_validator


class Architecture(BaseModel):
    language: str
    runtime: list[Literal["CLI", "GUI", "Web", "Library"]]
    framework: list[str]
    entrypoint: str

    @field_validator("runtime")
    @classmethod
    def validate_runtime(cls: type["Architecture"], v: list[str]) -> list[str]:
        allowed = {"CLI", "GUI", "Web", "Library"}
        for r in v:
            if r not in allowed:
                raise ValueError(f"Invalid runtime: {r}")
        return v


class Scope(BaseModel):
    core_features: list[str]
    optional_features: list[str]
    explicitly_excluded: list[str]


class ImplementationStep(BaseModel):
    step: int
    description: str
    produces: list[str]


class Proposal(BaseModel):
    goal_summary: str

    architecture: Architecture
    scope: Scope
    implementation_steps: list[ImplementationStep]

    uncertainties: list[str]
