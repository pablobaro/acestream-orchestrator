package vpn

import (
	"testing"

	"github.com/docker/docker/api/types/network"
)

func TestEffectiveDockerNetwork(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		configured string
		want       string
	}{
		{
			// An unset DOCKER_NETWORK means the container lands on Docker's
			// default bridge. Returning "" here is what made the firewall
			// allowlist a silent no-op for every default-configured install.
			name:       "empty falls back to the default bridge",
			configured: "",
			want:       "bridge",
		},
		{name: "explicit network is kept", configured: "aceserver", want: "aceserver"},
		{name: "whitespace is trimmed", configured: "  aceserver  ", want: "aceserver"},
		{name: "host mode has no bridge subnet", configured: "host", want: ""},
		{name: "none mode has no bridge subnet", configured: "none", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := effectiveDockerNetwork(tc.configured); got != tc.want {
				t.Fatalf("effectiveDockerNetwork(%q) = %q, want %q", tc.configured, got, tc.want)
			}
		})
	}
}

func TestSubnetsFromIPAM(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   []network.IPAMConfig
		want []string
	}{
		{
			name: "nil config yields nothing",
			in:   nil,
			want: nil,
		},
		{
			name: "single subnet",
			in:   []network.IPAMConfig{{Subnet: "172.18.0.0/16"}},
			want: []string{"172.18.0.0/16"},
		},
		{
			name: "blank entries are skipped",
			in: []network.IPAMConfig{
				{Subnet: ""},
				{Subnet: "172.18.0.0/16"},
				{Subnet: "  "},
			},
			want: []string{"172.18.0.0/16"},
		},
		{
			name: "dual stack keeps both families",
			in: []network.IPAMConfig{
				{Subnet: "172.18.0.0/16"},
				{Subnet: "fd00::/64"},
			},
			want: []string{"172.18.0.0/16", "fd00::/64"},
		},
		{
			name: "duplicates are collapsed",
			in: []network.IPAMConfig{
				{Subnet: "172.18.0.0/16"},
				{Subnet: "172.18.0.0/16"},
			},
			want: []string{"172.18.0.0/16"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := subnetsFromIPAM(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("subnetsFromIPAM() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("subnetsFromIPAM() = %v, want %v", got, tc.want)
				}
			}
		})
	}
}
