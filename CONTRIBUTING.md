# Contributing

## OpenSpec Workflow

Use the OpenSpec workflow: propose -> implement -> validate -> archive.
Archived changes are the step that updates canonical specs under openspec/specs/.
Spec layering in canonical specs is strict: Purpose = why (intent, decisions, non-goals), Requirements = what (normative behavior and scenarios).
Keep OpenSpec artifacts concise and rely on OpenSpec files for full details.

## Developing

Please install the `pre-commit` to enforce the code conventions and alignment.

```shell
pip install pre-commit
```

Install the required pre-commit hooks.

```shell
pre-commit install -t commit-msg
```
