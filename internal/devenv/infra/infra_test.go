package infra

import (
	"strings"
	"testing"
)

func TestRequireCgroupV2(t *testing.T) {
	if err := requireCgroupV2("cgroup2fs\n"); err != nil {
		t.Errorf("rejected cgroup v2: %v", err)
	}
	if err := requireCgroupV2("tmpfs\n"); err == nil || !strings.Contains(err.Error(), "cgroup v2") {
		t.Errorf("err = %v, want an explanation that cgroup v2 is required", err)
	}
}
