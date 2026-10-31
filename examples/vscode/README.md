# VS Code tasks

Copy [`tasks.json`](tasks.json) to `.vscode/tasks.json` in a recom project.
Then *Terminal → Run Task* offers:

- **recom: run current Python file** runs `recom python3 <file>` from the file's
  directory, so outputs land next to it.
- **recom: run with TTY** is the same in an interactive terminal (prompts, colors).
- **recom: status** shows the backend, last run and pending outputs.

`.vscode/` is excluded from upload by default.
