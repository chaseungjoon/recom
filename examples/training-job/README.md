# Detached training job

```sh
recom init
JOB=$(recom --detach python3 -u train.py --epochs 20)   # prints the job id
recom jobs
recom logs $JOB -f                # follow; Ctrl-C stops following, not training
recom attach $JOB                 # live tmux view; detach with your tmux prefix + d
recom stop $JOB                   # SIGTERM (checkpoint is saved), SIGKILL after --grace
```

Close the laptop whenever you like: the job keeps running and records its
outputs on the backend. Your next `recom` command in this project downloads
`runs/metrics.csv` and the final checkpoint. Checkpoints above
`max_file_size` (100MB by default) stay on the backend as *held* outputs:

```sh
recom status                      # lists held files and why
recom pull checkpoints/last.pt    # fetch one explicitly
```
