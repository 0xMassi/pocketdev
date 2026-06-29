package hetzner

import (
	"reflect"
	"testing"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

func TestCandidateLocations(t *testing.T) {
	cases := []struct {
		region string
		arch   hcloud.Architecture
		want   []string
	}{
		{"nbg1", hcloud.ArchitectureX86, []string{"nbg1", "fsn1", "hel1"}},
		{"fsn1", hcloud.ArchitectureX86, []string{"fsn1", "nbg1", "hel1"}},
		{"ash", hcloud.ArchitectureX86, []string{"ash", "hil"}},
		{"sin", hcloud.ArchitectureX86, []string{"sin"}},
		{"hel1", hcloud.ArchitectureARM, []string{"hel1", "nbg1", "fsn1"}}, // CAX: EU only
	}
	for _, c := range cases {
		got := candidateLocations(c.region, c.arch)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("candidateLocations(%q,%s) = %v, want %v", c.region, c.arch, got, c.want)
		}
		if got[0] != c.region {
			t.Errorf("chosen region %q must be tried first, got %v", c.region, got)
		}
	}
}
