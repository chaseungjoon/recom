"""A dependency-free stand-in for a training loop with checkpoints."""
import argparse
import json
import math
import os
import random
import signal
import time

parser = argparse.ArgumentParser()
parser.add_argument("--epochs", type=int, default=10)
parser.add_argument("--seconds-per-epoch", type=float, default=1.0)
args = parser.parse_args()

os.makedirs("runs", exist_ok=True)
os.makedirs("checkpoints", exist_ok=True)
state = {"epoch": 0, "loss": None}


def checkpoint(name):
    with open(f"checkpoints/{name}.json", "w") as f:
        json.dump(state, f)


def on_term(*_):
    print("SIGTERM: saving checkpoint and exiting", flush=True)
    checkpoint("interrupted")
    raise SystemExit(143)


signal.signal(signal.SIGTERM, on_term)

with open("runs/metrics.csv", "w") as metrics:
    metrics.write("epoch,loss\n")
    for epoch in range(1, args.epochs + 1):
        time.sleep(args.seconds_per_epoch)
        loss = math.exp(-epoch / 5) + random.random() * 0.01
        state.update(epoch=epoch, loss=loss)
        metrics.write(f"{epoch},{loss:.5f}\n")
        metrics.flush()
        print(f"epoch {epoch:3d}  loss {loss:.4f}", flush=True)
        if epoch % 5 == 0:
            checkpoint("last")

checkpoint("final")
print("done", flush=True)
