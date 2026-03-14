## Validation

Before responding, confirm:

1. All constraints satisfied
2. Output matches schema
3. No extra text outside JSON
4. If the task cannot be completed safely or correctly, return exactly: `TASK_FAILED`
5. If you cannot follow all constraints, return: `ERROR: constraint violation`
