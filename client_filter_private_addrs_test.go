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
			name: "mixed_private_and_public_ipv4",
			input: []string{
				"/ip4/10.0.0.1/tcp/4001",    // RFC1918
				"/ip4/8.8.8.8/tcp/4001",     // public
				"/ip4/172.16.0.1/tcp/4001",  // RFC1918
				"/ip4/1.1.1.1/tcp/4001",     // public
				"/ip4/192.168.1.1/tcp/4001", // RFC1918
				"/ip4/127.0.0.1/tcp/4001",   // loopback
				"/ip4/169.254.1.1/tcp/4001", // link-local
			},
			expected: []string{
				"/ip4/8.8.8.8/tcp/4001",
				"/ip4/1.1.1.1/tcp/4001",
			},
		},
		{
			name: "mixed_private_and_public_ipv6",
			input: []string{
				"/ip6/fc00::1/tcp/4001",              // unique local
				"/ip6/2001:4860:4860::8888/tcp/4001", // public
				"/ip6/fe80::1/tcp/4001",              // link-local
			},
			expected: []string{
				"/ip6/2001:4860:4860::8888/tcp/4001",
			},
		},
		{
			name: "rfc1918_172_range_boundaries",
			input: []string{
				"/ip4/172.15.0.1/tcp/4001",     // not private (below range)
				"/ip4/172.16.0.1/tcp/4001",     // private (start of range)
				"/ip4/172.31.255.255/tcp/4001", // private (end of range)
				"/ip4/172.32.0.1/tcp/4001",     // not private (above range)
			},
			expected: []string{
				"/ip4/172.15.0.1/tcp/4001",
				"/ip4/172.32.0.1/tcp/4001",
			},
		},
		{
			name: "edge_cases",
			input: []string{
				"invalid-addr",           // invalid address
				"/ip4/8.8.8.8/tcp/4001",  // valid public
				"/ip4/10.0.0.1/tcp/4001", // valid private
			},
			expected: []string{
				"/ip4/8.8.8.8/tcp/4001",
			},
		},
		{
			name:     "empty_input",
			input:    []string{},
			expected: []string{},
		},
		{
			name: "all_private",
			input: []string{
				"/ip4/10.0.0.1/tcp/4001",
				"/ip4/192.168.1.1/tcp/4001",
			},
			expected: []string{},
		},
		{
			name: "all_public",
			input: []string{
				"/ip4/8.8.8.8/tcp/4001",
				"/ip6/2001:4860:4860::8888/tcp/4001",
			},
			expected: []string{
				"/ip4/8.8.8.8/tcp/4001",
				"/ip6/2001:4860:4860::8888/tcp/4001",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterPrivateAddrs(testParseMultiaddrs(tt.input))
			assertAddrsEqual(t, tt.expected, result)
		})
	}
}

// testParseMultiaddrs converts string addresses to multiaddr.Multiaddr, skipping invalid ones
func testParseMultiaddrs(addrs []string) []multiaddr.Multiaddr {
	result := make([]multiaddr.Multiaddr, 0, len(addrs))
	for _, addrStr := range addrs {
		addr, err := multiaddr.NewMultiaddr(addrStr)
		if err == nil {
			result = append(result, addr)
		}
	}
	return result
}

// assertAddrsEqual compares expected string addresses with actual multiaddrs
func assertAddrsEqual(t *testing.T, expected []string, actual []multiaddr.Multiaddr) {
	t.Helper()

	actualStrs := make([]string, len(actual))
	for i, addr := range actual {
		actualStrs[i] = addr.String()
	}

	if len(actualStrs) != len(expected) {
		t.Errorf("Expected %d addresses, got %d.\nExpected: %v\nGot: %v",
			len(expected), len(actualStrs), expected, actualStrs)
		return
	}

	for i, expectedAddr := range expected {
		if actualStrs[i] != expectedAddr {
			t.Errorf("Address %d: expected %s, got %s", i, expectedAddr, actualStrs[i])
		}
	}
}

func TestIsPrivateIP(t *testing.T) {
	privateIPs := []string{
		// RFC1918
		"10.0.0.1", "10.255.255.255",
		"172.16.0.1", "172.31.255.255",
		"192.168.1.1", "192.168.255.255",
		// Link-local
		"169.254.1.1",
		// Loopback
		"127.0.0.1", "127.255.255.255",
		// IPv6 unique local and link-local
		"fc00::1", "fd00::1", "fe80::1", "::1",
	}

	publicIPs := []string{
		// Public IPv4
		"8.8.8.8", "1.1.1.1", "208.67.222.222",
		// RFC1918 172.x boundaries (not in 16-31 range)
		"172.15.0.1", "172.32.0.1",
		// Public IPv6
		"2001:4860:4860::8888", "2606:4700:4700::1111",
	}

	for _, ipStr := range privateIPs {
		t.Run(ipStr, func(t *testing.T) {
			ip := net.ParseIP(ipStr)
			if ip == nil {
				t.Fatalf("Failed to parse IP: %s", ipStr)
			}
			if !isPrivateIP(ip) {
				t.Errorf("isPrivateIP(%s) = false, want true", ipStr)
			}
		})
	}

	for _, ipStr := range publicIPs {
		t.Run(ipStr, func(t *testing.T) {
			ip := net.ParseIP(ipStr)
			if ip == nil {
				t.Fatalf("Failed to parse IP: %s", ipStr)
			}
			if isPrivateIP(ip) {
				t.Errorf("isPrivateIP(%s) = true, want false", ipStr)
			}
		})
	}
}
