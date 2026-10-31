# C/C++

```sh
recom init
recom gcc src/main.c -O3 -o bin/main   # compiled by the backend's gcc (Linux binary)
recom ./bin/main 1000000               # runs on the backend
recom make                             # or use the Makefile
```

`bin/main` also comes back to your machine as an output. It is a Linux
executable, so it only runs on the backend (recom never runs it locally).

CUDA works the same way when the backend has the toolkit:

```sh
recom nvcc kernels/saxpy.cu -o bin/saxpy && recom ./bin/saxpy
```
