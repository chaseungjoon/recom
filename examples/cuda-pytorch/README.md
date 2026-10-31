# CUDA PyTorch

The virtualenv lives **on the backend**; your laptop needs no Python at all.

```sh
recom init
# point commands at the backend's venv
sed -i.bak 's|^path = \[\]|path = [".venv/bin"]|' .recom.toml

# one-time environment setup, as a detached job (large download)
recom --detach --shell 'uv venv .venv && uv pip install --python .venv/bin/python torch --torch-backend=auto'
recom logs <job-id> -f

recom nvidia-smi                 # the backend's GPU
recom python3 gpu.py             # matmul benchmark on CUDA; outputs/gpu.json comes back
```

`.venv/` is never uploaded, downloaded or treated as an output. Without `uv`,
use `python3 -m venv .venv && .venv/bin/pip install torch`.
