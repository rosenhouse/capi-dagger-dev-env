package infra

import (
	"strings"
	"testing"
)

func TestRequireCgroupV2(t *testing.T) {
	if err := requireCgroupV2("63677270\n"); err != nil {
		t.Errorf("rejected cgroup2's filesystem magic: %v", err)
	}
	tmpfs := "1021994\n"
	if err := requireCgroupV2(tmpfs); err == nil || !strings.Contains(err.Error(), "cgroup v2") {
		t.Errorf("err = %v, want an explanation that cgroup v2 is required", err)
	}
}
