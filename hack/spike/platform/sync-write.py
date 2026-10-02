import os
import sys
import time

# Rewrites one 4 KiB block per 64 KiB of an existing file with O_DSYNC, as etcd's WAL does in place.
path = sys.argv[1]
blocks = os.path.getsize(path) // 65536
fd = os.open(path, os.O_WRONLY | os.O_DSYNC)
start = time.perf_counter()
for i in range(blocks):
    os.pwrite(fd, b"x" * 4096, i * 65536)
print(f"{(time.perf_counter() - start) / blocks * 1000:.2f} ms")
