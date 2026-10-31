# Python outputs

```sh
recom init
recom python3 src/run.py          # uploads, runs remotely, brings back outputs/result.json + plot.csv
cat outputs/result.json           # "host" is the backend, not this machine

cd src && recom python3 run.py    # runs in <replica>/src; outputs land in ./outputs here
```

Edit `src/run.py` and run again: only the changed file is uploaded, and only
files the run actually changed come back.
