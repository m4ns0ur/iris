import json
import logging
from pathlib import Path
import sys

from rich.console import Console
from rich.pretty import Pretty

# Global logger
logger = logging.getLogger("iris")
logger.setLevel(logging.INFO)

# Console handler placeholder (level set later based on debug)
handler = logging.StreamHandler(sys.stdout)
formatter = logging.Formatter("%(asctime)s [%(levelname)s] %(message)s")
handler.setFormatter(formatter)
logger.addHandler(handler)

# File handler
log_file = Path("iris_debug.log")
fh = logging.FileHandler(log_file)
fh.setFormatter(formatter)
fh.setLevel(logging.DEBUG)
logger.addHandler(fh)

console = Console()


def set_debug(enable: bool) -> None:
    logger.setLevel(logging.DEBUG if enable else logging.INFO)
    logger.debug(f"Debug mode enabled: {enable}")


def log_llm_payload(payload: dict) -> None:
    logger.debug("========== LLM REQUEST ==========")

    console.print(Pretty(payload))

    logger.debug(f"Model: {payload.get('model')}")
    logger.debug(f"Temperature: {payload.get('temperature')}")
    logger.debug(f"Max tokens: {payload.get('max_tokens')}")

    messages = payload.get("messages", [])

    for i, msg in enumerate(messages):
        role = msg.get("role")
        content = msg.get("content")

        logger.debug(f"\nMessage[{i}] role={role}")

        # Try parsing JSON content if applicable
        if isinstance(content, str):
            try:
                parsed = json.loads(content)
                logger.debug("Parsed JSON content:")
                console.print(Pretty(parsed))
            except Exception:
                logger.debug(f"Content preview:\n{content}")
        else:
            console.print(Pretty(content))

    logger.debug("=================================\n")


def log_llm_response(response: str) -> None:
    logger.debug("========== LLM RESPONSE ==========")

    try:
        parsed = json.loads(response)
        console.print(Pretty(parsed))

        choices = parsed.get("choices", [])

        for i, choice in enumerate(choices):
            message = choice.get("message", {})
            role = message.get("role")
            content = message.get("content")

            logger.debug(f"\nChoice[{i}] role={role}")

            if isinstance(content, str):
                try:
                    parsed_content = json.loads(content)
                    logger.debug("Parsed JSON content:")
                    console.print(Pretty(parsed_content, expand_all=True))
                except Exception:
                    logger.debug(f"Content preview:\n{content}")
            else:
                console.print(Pretty(content))
    except Exception:
        logger.debug(response)

    logger.debug("==================================\n")
