from copy import deepcopy
from typing import Any


class EnumRepairEngine:
    """
    Generic enum normalization and repair engine.

    Repairs:
    - Case mismatches
    - Pipe/comma separated enum strings
    - Invalid values removed or optionally redirected

    Designed to run BEFORE Pydantic validation.
    """

    def __init__(self) -> None:
        self._rules: dict[str, dict[str, Any]] = {}

    def register_enum_field(
        self,
        field_path: str,
        allowed_values: set[str],
        is_list: bool = False,
        redirect_invalid_to: str | None = None,
    ) -> None:
        """
        field_path: dotted path (e.g., "architecture.runtime")
        allowed_values: set of allowed enum values
        is_list: whether field is list of enums
        redirect_invalid_to: optional dotted path to move invalid values
        """
        self._rules[field_path] = {
            "allowed": allowed_values,
            "is_list": is_list,
            "redirect": redirect_invalid_to,
        }

    def repair(self, data: dict[str, Any]) -> dict[str, Any]:
        repaired = deepcopy(data)

        for field_path, rule in self._rules.items():
            self._repair_field(repaired, field_path, rule)

        return repaired

    def _repair_field(
        self, data: dict[str, Any], field_path: str, rule: dict[str, Any]
    ) -> None:
        keys = field_path.split(".")
        parent = self._navigate_to_parent(data, keys)
        if parent is None:
            return

        field_name = keys[-1]
        if field_name not in parent:
            return

        value = parent[field_name]
        allowed = rule["allowed"]
        is_list = rule["is_list"]
        redirect_path = rule["redirect"]

        if value is None:
            return

        # Normalize input to list
        values = self._normalize_to_list(value) if is_list else [value]

        valid_values = []
        invalid_values = []

        for v in values:
            normalized = str(v).strip().upper()
            if normalized in allowed:
                valid_values.append(normalized)
            else:
                invalid_values.append(v)

        # Apply valid values
        if is_list:
            parent[field_name] = valid_values
        else:
            parent[field_name] = valid_values[0] if valid_values else None

        # Redirect invalid values if configured
        if redirect_path and invalid_values:
            self._append_to_field(data, redirect_path, invalid_values)

    def _normalize_to_list(self, value: str | list[Any]) -> list[Any]:
        if isinstance(value, list):
            return value

        # Split on common separators
        separators = ["|", ",", "/"]
        text = str(value)
        for sep in separators:
            if sep in text:
                return [v.strip() for v in text.split(sep)]

        return [text]

    def _navigate_to_parent(self, data: dict[str, Any], keys: list[str]) -> dict | None:
        current = data
        for key in keys[:-1]:
            if not isinstance(current, dict) or key not in current:
                return None
            current = current[key]
        return current

    def _append_to_field(
        self, data: dict[str, Any], field_path: str, values: list[Any]
    ) -> None:
        keys = field_path.split(".")
        parent = self._navigate_to_parent(data, keys)
        if parent is None:
            return

        field_name = keys[-1]
        existing = parent.get(field_name)

        if existing is None:
            parent[field_name] = values
        elif isinstance(existing, list):
            parent[field_name].extend(values)
        else:
            parent[field_name] = [existing, *values]
