package staggercast

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/johnstarich/go/dns/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testTimeout = 5 * time.Second

func dialUDP(t *testing.T, address string) PacketConn {
	conn, err := net.Dial("udp", address)
	require.NoError(t, err)
	require.Implements(t, (*PacketConn)(nil), conn)
	return conn.(PacketConn)
}

func TestNew(t *testing.T) {
	conns := []PacketConn{
		dialUDP(t, "1.2.3.4:53"),
		dialUDP(t, "5.6.7.8:53"),
	}
	conn := New(conns)
	require.IsType(t, &staggerConn{}, conn)
	sConn := conn.(*staggerConn)

	assert.Equal(t, conns, sConn.conns)
	assert.Equal(t, uint64(2), sConn.connCount.Load())
	if assert.NotNil(t, sConn.tickerCancel) {
		sConn.tickerCancel()
	}

	require.Len(t, sConn.replay, 2)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	for _, channel := range sConn.replay {
		select {
		case _, open := <-channel:
			assert.False(t, open)
		case <-ctx.Done():
			require.NoError(t, ctx.Err())
		}
	}

	assert.PanicsWithValue(t, "connection count must be non-zero", func() {
		New(nil)
	})
}

func TestDialDNS(t *testing.T) {
	type dnsServer struct {
		delay     time.Duration
		hostnames map[string][]string
	}
	for _, tc := range []struct {
		description string
		servers     []dnsServer
		lookup      string
		expectAddrs []string
		expectErr   string
	}{
		{
			description: "1 working nameserver",
			servers: []dnsServer{
				{hostnames: map[string][]string{
					"hi.local.": {"1.2.3.4"},
				}},
			},
			lookup:      "hi.local",
			expectAddrs: []string{"1.2.3.4"},
		},
		{
			description: "1 unresponsive and 1 working nameserver",
			servers: []dnsServer{
				{delay: 30 * time.Second, hostnames: map[string][]string{
					"hi.local.": {"5.6.7.8"},
				}},
				{hostnames: map[string][]string{
					"hi.local.": {"1.2.3.4"},
				}},
			},
			lookup:      "hi.local",
			expectAddrs: []string{"1.2.3.4"},
		},
		{
			description: "1 unresponsive nameserver",
			servers: []dnsServer{
				{delay: 30 * time.Second, hostnames: map[string][]string{
					"hi.local.": {"5.6.7.8"},
				}},
			},
			lookup:    "hi.local",
			expectErr: "all connections have failed for \"write\": write udp [::1]:",
		},
		{
			description: "2 unresponsive nameservers",
			servers: []dnsServer{
				{delay: 30 * time.Second, hostnames: map[string][]string{
					"hi.local.": {"1.2.3.4"},
				}},
				{delay: 30 * time.Second, hostnames: map[string][]string{
					"hi.local.": {"5.6.7.8"},
				}},
			},
			lookup:    "hi.local",
			expectErr: "all connections have failed for \"write\": write udp [::1]:",
		},
	} {
		tc := tc // fix parallel access of loop variable
		t.Run(tc.description, func(t *testing.T) {
			t.Parallel()
			var servers []string
			for _, server := range tc.servers {
				addr, cancel := testhelpers.StartDNSServer(t, server.delay, server.hostnames)
				defer cancel()
				servers = append(servers, addr)
			}
			t.Logf("DNS servers, in-order: %+v", servers)

			res := &net.Resolver{
				PreferGo: true,
				Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
					var conns []PacketConn
					for _, addr := range servers {
						conns = append(conns, dialUDP(t, addr))
					}
					return New(conns), nil
				},
			}

			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()

			addrs, err := res.LookupHost(ctx, tc.lookup)
			if tc.expectErr != "" {
				assert.Equal(t, tc.expectAddrs, addrs)
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.expectErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.expectAddrs, addrs)
		})
	}
}
