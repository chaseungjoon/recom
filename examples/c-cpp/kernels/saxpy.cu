#include <cstdio>
#include <cuda_runtime.h>

__global__ void saxpy(int n, float a, const float *x, float *y) {
    int i = blockIdx.x * blockDim.x + threadIdx.x;
    if (i < n) y[i] = a * x[i] + y[i];
}

int main() {
    const int n = 1 << 24;
    float *x, *y;
    cudaMallocManaged(&x, n * sizeof(float));
    cudaMallocManaged(&y, n * sizeof(float));
    for (int i = 0; i < n; i++) { x[i] = 1.0f; y[i] = 2.0f; }
    saxpy<<<(n + 255) / 256, 256>>>(n, 2.0f, x, y);
    cudaDeviceSynchronize();
    cudaDeviceProp prop;
    cudaGetDeviceProperties(&prop, 0);
    printf("y[0] = %.1f (expected 4.0) on %s\n", y[0], prop.name);
    cudaFree(x);
    cudaFree(y);
    return 0;
}
