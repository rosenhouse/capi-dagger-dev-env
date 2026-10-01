import time

n = 1_000_000
start = time.perf_counter_ns()
for _ in range(n):
    time.monotonic_ns()
print(f"{(time.perf_counter_ns() - start) / n:.0f} ns")
