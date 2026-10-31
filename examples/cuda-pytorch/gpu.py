"""Times a CUDA matmul and saves the result; fails clearly without a GPU."""
import json
import os
import time

import torch

if not torch.cuda.is_available():
    raise SystemExit("CUDA is not available on this backend (check `recom nvidia-smi` and your torch build)")

dev = torch.device("cuda")
n = 4096
a = torch.randn(n, n, device=dev)
b = torch.randn(n, n, device=dev)
torch.cuda.synchronize()
start = time.time()
for _ in range(20):
    c = a @ b
torch.cuda.synchronize()
elapsed = time.time() - start

result = {
    "device": torch.cuda.get_device_name(0),
    "torch": torch.__version__,
    "cuda": torch.version.cuda,
    "tflops": round(20 * 2 * n**3 / elapsed / 1e12, 1),
}
os.makedirs("outputs", exist_ok=True)
with open("outputs/gpu.json", "w") as f:
    json.dump(result, f, indent=2)
torch.save(c[:64, :64].cpu(), "outputs/sample.pt")
print(result)
