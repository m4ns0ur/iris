import logging
from pathlib import Path
import sys

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


def set_debug(enable: bool) -> None:
    logger.setLevel(logging.DEBUG if enable else logging.INFO)
    logger.debug(f"Debug mode enabled: {enable}")
