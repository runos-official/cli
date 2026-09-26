package cmd

import (
	"testing"

	"github.com/runos-official/cli/internal/deploy"
)

// FCR 753: after the first deploy the CLI stamped cpuLimitMc/memoryLimitMb
// next to a NAMED class. apps pull writes only the class id for a named
// class, so apps diff reported the stamped numbers as drift. The stamp must
// write the same canonical shape as pull.
func TestApplySynthesizedResources(t *testing.T) {
	server := func(rrc string) *deploy.AppShow {
		return &deploy.AppShow{ResourceRequirementClassID: rrc, CPURequestMc: 100, CPULimitMc: 1000, MemoryRequestMb: 128, MemoryLimitMb: 2048}
	}

	t.Run("named class records only the class id", func(t *testing.T) {
		c := &deploy.DeployConfig{}
		if !applySynthesizedResources(c, server("app.sl1.beff")) {
			t.Fatal("changed = false, want true")
		}
		if c.ResourceRequirementClassID != "app.sl1.beff" {
			t.Errorf("class = %q", c.ResourceRequirementClassID)
		}
		if c.CPURequestMc != 0 || c.CPULimitMc != 0 || c.MemoryRequestMb != 0 || c.MemoryLimitMb != 0 {
			t.Errorf("named class stamped resource numbers: %+v", c)
		}
	})

	t.Run("custom records every dimension", func(t *testing.T) {
		c := &deploy.DeployConfig{}
		applySynthesizedResources(c, server("custom"))
		if c.ResourceRequirementClassID != "custom" || c.CPULimitMc != 1000 || c.MemoryLimitMb != 2048 ||
			c.CPURequestMc != 100 || c.MemoryRequestMb != 128 {
			t.Errorf("custom stamp = %+v", c)
		}
	})

	t.Run("user-set values are never overwritten", func(t *testing.T) {
		c := &deploy.DeployConfig{CPULimitMc: 500}
		if applySynthesizedResources(c, server("custom")) {
			t.Error("changed = true for a yaml that already carries resources")
		}
	})
}
