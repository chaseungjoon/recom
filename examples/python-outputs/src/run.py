"""Writes a JSON summary and a CSV next to the current directory."""
import json
import math
import os
import platform
import socket

os.makedirs("outputs", exist_ok=True)

with open("outputs/plot.csv", "w") as f:
    f.write("x,sin\n")
    for i in range(100):
        x = i / 10
        f.write(f"{x:.1f},{math.sin(x):.4f}\n")

summary = {
    "host": socket.gethostname(),
    "machine": platform.machine(),
    "python": platform.python_version(),
    "cwd": os.getcwd(),
}
with open("outputs/result.json", "w") as f:
    json.dump(summary, f, indent=2)

print(json.dumps(summary))
