#!/usr/bin/env bash
echo "OBS: runner $(uname -m) $(nproc) cpus, $(free -g | awk '/Mem/{print $2}') GB"
echo "OBS: cgroup $(stat -fc %T /sys/fs/cgroup)"
echo "OBS: inotify $(cat /proc/sys/fs/inotify/max_user_instances) $(cat /proc/sys/fs/inotify/max_user_watches)"
df -h / | tail -1
