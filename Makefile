.PHONY: all

all: lint format type

lint:
	-uv run ruff check . --fix

format:
	uv run ruff format .

type:
	uv run mypy iris/ --explicit-package-bases

test:
	uv run pytest
