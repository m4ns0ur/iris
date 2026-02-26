.PHONY: lint format type test

lint:
	uv run ruff check . --fix
format:
	uv run ruff format .
type:
	uv run mypy iris/ --explicit-package-bases
test:
	uv run pytest
