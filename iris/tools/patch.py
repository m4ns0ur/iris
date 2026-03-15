import re

PATCH_BLOCK = re.compile(
    r"<<<<<<< SEARCH\n(.*?)\n=======\n(.*?)\n>>>>>>> REPLACE",
    re.DOTALL,
)


def apply_search_replace_patch(original: str, patch: str) -> str:
    """
    Apply SEARCH/REPLACE blocks to a file.
    """

    blocks = PATCH_BLOCK.findall(patch)

    if not blocks:
        raise ValueError("No patch blocks found")

    content = original

    for search, replace in blocks:
        if search not in content:
            # try stripped match
            stripped = search.strip()
            for line in content.splitlines():
                if stripped in line:
                    content = content.replace(line, replace, 1)
                    break
            else:
                raise ValueError(f"Search block not found in file:\n{search[:120]}")

        content = content.replace(search, replace, 1)

    return content
