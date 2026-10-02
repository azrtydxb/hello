package sip

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/valkey-io/valkey-go"
)

// Presence publishes this node and lists the cluster's SIP nodes, so the
// edge proxy only relays for known peers.
type Presence interface {
	// Announce records nodeID at its advertised addr for ttl.
	Announce(ctx context.Context, nodeID, addr string, ttl time.Duration) error
	// Nodes returns the advertised addresses of the live nodes.
	Nodes(ctx context.Context) ([]string, error)
}

const nodePrefix = "hello:node:"

// ValkeyPresence keeps node records at hello:node:{nodeID} = advertised
// address, each with a TTL its node refreshes.
type ValkeyPresence struct{ Client valkey.Client }

// Announce sets this node's record.
func (p ValkeyPresence) Announce(ctx context.Context, nodeID, addr string, ttl time.Duration) error {
	return p.Client.Do(ctx, p.Client.B().Set().Key(nodePrefix+nodeID).Value(addr).Px(ttl).Build()).Error()
}

// Nodes lists the advertised addresses of the nodes with a live record.
func (p ValkeyPresence) Nodes(ctx context.Context) ([]string, error) {
	var keys []string
	var cursor uint64
	for {
		e, err := p.Client.Do(ctx, p.Client.B().Scan().Cursor(cursor).Match(nodePrefix+"*").Count(100).Build()).AsScanEntry()
		if err != nil {
			return nil, fmt.Errorf("presence scan: %w", err)
		}
		keys = append(keys, e.Elements...)
		if e.Cursor == 0 {
			break
		}
		cursor = e.Cursor
	}
	if len(keys) == 0 {
		return nil, nil
	}
	vals, err := p.Client.Do(ctx, p.Client.B().Mget().Key(keys...).Build()).ToArray()
	if err != nil {
		return nil, fmt.Errorf("presence mget: %w", err)
	}
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if s, err := v.ToString(); err == nil && s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

// peerSet holds the cluster's SIP nodes as advertised (host:port, possibly
// a hostname) and as resolved IP:port, which is what packets come from.
type peerSet map[string]bool

func (p peerSet) has(addr string) bool {
	if p[strings.ToLower(addr)] {
		return true
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return p[net.JoinHostPort(ip.String(), port)]
	}
	return false
}

// resolvePeers builds the peer set from advertised addresses; names that do
// not resolve are kept only in their advertised form.
func resolvePeers(ctx context.Context, addrs []string) peerSet {
	set := peerSet{}
	for _, a := range addrs {
		host, port, err := net.SplitHostPort(a)
		if err != nil {
			continue
		}
		if p, err := strconv.Atoi(port); err != nil || p <= 0 {
			continue
		}
		set[strings.ToLower(a)] = true
		if ip := net.ParseIP(host); ip != nil {
			set[net.JoinHostPort(ip.String(), port)] = true
			continue
		}
		ips, err := net.DefaultResolver.LookupHost(ctx, host)
		if err != nil {
			continue
		}
		for _, ip := range ips {
			if parsed := net.ParseIP(ip); parsed != nil {
				set[net.JoinHostPort(parsed.String(), port)] = true
			}
		}
	}
	return set
}

// peers is the cached peer set, refreshed in the background every
// PeerRefresh. A miss may trigger one extra refresh (see Server.isPeer).
type peers struct {
	p atomic.Pointer[peerSet]
	// lastMiss is when a miss last forced a refresh (unix nanoseconds).
	lastMiss atomic.Int64
}

// missRefreshEvery bounds refreshes forced by unknown sources, so a stranger
// sending junk cannot turn every datagram into Valkey and DNS calls.
const missRefreshEvery = time.Second

func (ps *peers) set(s peerSet) { ps.p.Store(&s) }

func (ps *peers) has(addr string) bool {
	s := ps.p.Load()
	return s != nil && s.has(addr)
}

// refreshPeers announces this node and reloads the peer set. This node is
// always a peer, so a node works alone even when Valkey is unreachable.
func (s *Server) refreshPeers(ctx context.Context) {
	addrs := []string{s.cfg.AdvertisedAddr, s.laddr.String()}
	if p := s.deps.Presence; p != nil {
		actx, cancel := context.WithTimeout(ctx, 2*time.Second)
		if err := p.Announce(actx, s.cfg.NodeID, s.cfg.AdvertisedAddr, s.cfg.NodeTTL); err != nil {
			s.log.Debug("presence announce failed", "error", err)
		}
		nodes, err := p.Nodes(actx)
		cancel()
		if err != nil {
			s.log.Debug("presence list failed; keeping the previous peers", "error", err)
			if s.peers.p.Load() != nil {
				return
			}
		}
		addrs = append(addrs, nodes...)
	}
	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	s.peers.set(resolvePeers(rctx, addrs))
}

// isPeer reports whether addr is a cluster SIP node. On a miss it refreshes
// the peer set at most once per missRefreshEvery and checks again: nodes
// that start together each come up before the other has announced itself,
// and would otherwise distrust each other until the next background refresh.
func (s *Server) isPeer(addr string) bool {
	if s.peers.has(addr) {
		return true
	}
	now := time.Now().UnixNano()
	last := s.peers.lastMiss.Load()
	if now-last < int64(missRefreshEvery) || !s.peers.lastMiss.CompareAndSwap(last, now) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.StateTimeout+2*time.Second)
	defer cancel()
	s.refreshPeers(ctx)
	return s.peers.has(addr)
}

func (s *Server) peerLoop(ctx context.Context) {
	t := time.NewTicker(s.cfg.PeerRefresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.refreshPeers(ctx)
		}
	}
}
