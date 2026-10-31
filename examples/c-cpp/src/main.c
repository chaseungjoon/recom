#include <stdio.h>
#include <stdlib.h>

/* Counts primes below N with a sieve and prints where it ran. */
int main(int argc, char **argv) {
    long n = argc > 1 ? atol(argv[1]) : 100000;
    char *composite = calloc(n, 1);
    long count = 0;
    for (long i = 2; i < n; i++) {
        if (composite[i]) continue;
        count++;
        for (long j = i * i; j < n; j += i) composite[j] = 1;
    }
    char host[256] = "?";
    FILE *f = fopen("/etc/hostname", "r");
    if (f) { if (fscanf(f, "%255s", host) != 1) host[0] = '?'; fclose(f); }
    printf("%ld primes below %ld (computed on %s)\n", count, n, host);
    free(composite);
    return 0;
}
