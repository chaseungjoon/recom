# CUDA PyTorch

The virtualenv lives **on the backend**; your laptop needs no Python at all.
`requirements.txt` lists `torch` (PyPI's Linux wheels include CUDA).

```sh
recom init                       # detects requirements.txt, builds .venv on the backend (large download)
recom nvidia-smi                 # the backend's GPU
recom python3 gpu.py             # matmul benchmark on CUDA; outputs/gpu.json comes back
```

`python3` resolves to the backend's `.venv/bin/python3` because `recom init`
recorded `path = [".venv/bin"]`. Change `requirements.txt` and the next run
updates the environment first. `.venv/` is never uploaded, downloaded or
treated as an output.

For a specific CUDA build, use PyTorch's index in `requirements.txt`:

```
--index-url https://download.pytorch.org/whl/cu128
torch
```
