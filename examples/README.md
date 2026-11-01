# Examples

Each directory is a tiny standalone project. Copy one somewhere, `cd` into it,
`recom init`, and run the commands shown. The Python, C, PyTorch and training
examples were run from an ARM Linux client against an Ubuntu 24.04 +
RTX 5070 backend. The CUDA C kernel needs `nvcc`, which that backend did not
have, and the VS Code tasks were not tested in an editor.

| Example | Shows |
|---|---|
| [python-outputs](python-outputs/) | Outputs coming back automatically; nested working directories |
| [c-cpp](c-cpp/) | Compiling with the backend's gcc/make and running the binary remotely |
| [cuda-pytorch](cuda-pytorch/) | A backend virtualenv built automatically from requirements.txt, and a CUDA PyTorch script |
| [training-job](training-job/) | A long detached training run with checkpoints, logs, attach and stop |
| [vscode](vscode/) | Editor tasks that run the current file through recom |
