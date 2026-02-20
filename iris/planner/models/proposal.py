from typing import Literal

from pydantic import BaseModel, Field, field_validator


class Architecture(BaseModel):
    language: str
    runtime: list[Literal["CLI", "GUI", "Web", "Library"]]
    framework: str
    project_structure: Literal["single_file", "modular", "package"]
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


class Constraints(BaseModel):
    python_version: str | None = None
    external_dependencies_allowed: bool = True
    testing_required: bool = False


class ExecutionStrategy(BaseModel):
    microtask_granularity: Literal["file_level"]
    max_steps: int


class ImplementationStep(BaseModel):
    step: int
    description: str
    produces: list[str]
    depends_on: list[int]


class Proposal(BaseModel):
    project_type: Literal["new_project", "modify_existing"]
    goal_summary: str

    architecture: Architecture
    scope: Scope
    constraints: Constraints
    execution_strategy: ExecutionStrategy
    implementation_steps: list[dict]

    confidence_score: float = Field(ge=0.0, le=1.0)
    uncertainties: list[str]
