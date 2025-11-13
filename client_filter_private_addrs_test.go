package p2p

import (
	"net"
	"testing"

	"github.com/multiformats/go-multiaddr"
)

func TestFilterPrivateAddrs(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name: "filter_out_private_ipv4",
			input: []string{
				"/ip4/10.0.0.1/tcp/4001",
				"/ip4/8.8.8.8/tcp/4001",
				"/ip4/172.16.0.1/tcp/4001",
				"/ip4/1.1.1.1/tcp/4001",
			},
			expected: []string{
				"/ip4/8.8.8.8/tcp/4001",
				"/ip4/1.1.1.1/tcp/4001",
			},
		},
		{
			name: "filter_out_localhost",
			input: []string{
				"/ip4/127.0.0.1/tcp/4001",
				"/ip4/8.8.8.8/tcp/4001",
			},
			expected: []string{
				"/ip4/8.8.8.8/tcp/4001",
			},
		},
		{
			name: "filter_out_link_local",
			input: []string{
				"/ip4/169.254.1.1/tcp/4001",
				"/ip4/8.8.8.8/tcp/4001",
			},
			expected: []string{
				"/ip4/8.8.8.8/tcp/4001",
			},
		},
		{
			name: "filter_out_192_168",
			input: []string{
				"/ip4/192.168.1.1/tcp/4001",
				"/ip4/8.8.8.8/tcp/4001",
			},
			expected: []string{
				"/ip4/8.8.8.8/tcp/4001",
			},
		},
		{
			name: "filter_out_ipv6_private",
			input: []string{
				"/ip6/fc00::1/tcp/4001",
				"/ip6/2001:4860:4860::8888/tcp/4001",
			},
			expected: []string{
				"/ip6/2001:4860:4860::8888/tcp/4001",
			},
		},
		{
			name: "filter_out_ipv6_link_local",
			input: []string{
				"/ip6/fe80::1/tcp/4001",
				"/ip6/2001:4860:4860::8888/tcp/4001",
			},
			expected: []string{
				"/ip6/2001:4860:4860::8888/tcp/4001",
			},
		},
		{
			name: "all_private_returns_empty",
			input: []string{
				"/ip4/10.0.0.1/tcp/4001",
				"/ip4/192.168.1.1/tcp/4001",
				"/ip4/127.0.0.1/tcp/4001",
			},
			expected: []string{},
		},
		{
			name: "all_public_returns_all",
			input: []string{
				"/ip4/8.8.8.8/tcp/4001",
				"/ip4/1.1.1.1/tcp/4001",
				"/ip6/2001:4860:4860::8888/tcp/4001",
			},
			expected: []string{
				"/ip4/8.8.8.8/tcp/4001",
				"/ip4/1.1.1.1/tcp/4001",
				"/ip6/2001:4860:4860::8888/tcp/4001",
			},
		},
		{
			name:     "empty_input_returns_empty",
			input:    []string{},
			expected: []string{},
		},
		{
			name: "mixed_valid_and_invalid",
			input: []string{
				"/ip4/8.8.8.8/tcp/4001",
				"invalid-addr",
				"/ip4/10.0.0.1/tcp/4001",
			},
			expected: []string{
				"/ip4/8.8.8.8/tcp/4001",
			},
		},
		{
			name: "edge_case_172_15_not_filtered",
			input: []string{
				"/ip4/172.15.0.1/tcp/4001", // Not in 172.16-31 range
				"/ip4/8.8.8.8/tcp/4001",
			},
			expected: []string{
				"/ip4/172.15.0.1/tcp/4001",
				"/ip4/8.8.8.8/tcp/4001",
			},
		},
		{
			name: "edge_case_172_32_not_filtered",
			input: []string{
				"/ip4/172.32.0.1/tcp/4001", // Not in 172.16-31 range
				"/ip4/8.8.8.8/tcp/4001",
			},
			expected: []string{
				"/ip4/172.32.0.1/tcp/4001",
				"/ip4/8.8.8.8/tcp/4001",
			},
		},
		{
			name: "edge_case_172_16_filtered",
			input: []string{
				"/ip4/172.16.0.1/tcp/4001",
				"/ip4/8.8.8.8/tcp/4001",
			},
			expected: []string{
				"/ip4/8.8.8.8/tcp/4001",
			},
		},
		{
			name: "edge_case_172_31_filtered",
			input: []string{
				"/ip4/172.31.255.255/tcp/4001",
				"/ip4/8.8.8.8/tcp/4001",
			},
			expected: []string{
				"/ip4/8.8.8.8/tcp/4001",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Convert string multiaddrs to multiaddr.Multiaddr
			inputAddrs := make([]multiaddr.Multiaddr, 0, len(tt.input))
			for _, addrStr := range tt.input {
				addr, err := multiaddr.NewMultiaddr(addrStr)
				if err != nil {
					// Skip invalid addresses in input
					continue
				}
				inputAddrs = append(inputAddrs, addr)
			}

			// Call filterPrivateAddrs
			result := filterPrivateAddrs(inputAddrs)

			// Convert result back to strings for comparison
			resultStrs := make([]string, 0, len(result))
			for _, addr := range result {
				resultStrs = append(resultStrs, addr.String())
			}

			// Check length
			if len(resultStrs) != len(tt.expected) {
				t.Errorf("Expected %d addresses, got %d.\nExpected: %v\nGot: %v",
					len(tt.expected), len(resultStrs), tt.expected, resultStrs)
				return
			}

			// Check each address
			for i, expectedAddr := range tt.expected {
				if resultStrs[i] != expectedAddr {
					t.Errorf("Address %d: expected %s, got %s", i, expectedAddr, resultStrs[i])
				}
			}
		})
	}
}

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		name     string
		ip       string
		expected bool
	}{
		// RFC1918 private networks
		{"10.0.0.1", "10.0.0.1", true},
		{"10.255.255.255", "10.255.255.255", true},
		{"172.16.0.1", "172.16.0.1", true},
		{"172.31.255.255", "172.31.255.255", true},
		{"192.168.1.1", "192.168.1.1", true},
		{"192.168.255.255", "192.168.255.255", true},

		// Edge cases for 172.x range
		{"172.15.0.1_not_private", "172.15.0.1", false},
		{"172.32.0.1_not_private", "172.32.0.1", false},

		// Link-local
		{"169.254.1.1", "169.254.1.1", true},

		// Loopback
		{"127.0.0.1", "127.0.0.1", true},
		{"127.255.255.255", "127.255.255.255", true},

		// Public IPs
		{"8.8.8.8", "8.8.8.8", false},
		{"1.1.1.1", "1.1.1.1", false},
		{"208.67.222.222", "208.67.222.222", false},

		// IPv6 unique local (fc00::/7)
		{"fc00::1", "fc00::1", true},
		{"fd00::1", "fd00::1", true},

		// IPv6 link-local (fe80::/10)
		{"fe80::1", "fe80::1", true},

		// IPv6 loopback
		{"::1", "::1", true},

		// IPv6 public
		{"2001:4860:4860::8888", "2001:4860:4860::8888", false},
		{"2606:4700:4700::1111", "2606:4700:4700::1111", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := parseIP(tt.ip)
			if ip == nil {
				t.Fatalf("Failed to parse IP: %s", tt.ip)
			}
			result := isPrivateIP(ip)
			if result != tt.expected {
				t.Errorf("isPrivateIP(%s) = %v, want %v", tt.ip, result, tt.expected)
			}
		})
	}
}

func parseIP(s string) net.IP {
	return net.ParseIP(s)
}
