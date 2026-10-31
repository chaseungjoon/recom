CFLAGS ?= -O3 -Wall

bin/main: src/main.c
	mkdir -p bin
	$(CC) $(CFLAGS) $< -o $@

run: bin/main
	./bin/main 10000000

.PHONY: run
