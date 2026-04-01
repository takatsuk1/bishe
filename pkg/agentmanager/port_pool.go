package agentmanager

import (
	"fmt"
	"sync"
)

type PortPool struct {
	base    int
	max     int
	used    map[int]bool
	mu      sync.RWMutex
}

func NewPortPool(base, max int) *PortPool {
	return &PortPool{
		base: base,
		max:  max,
		used: make(map[int]bool),
	}
}

func (p *PortPool) Allocate() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for port := p.base; port <= p.max; port++ {
		if !p.used[port] {
			p.used[port] = true
			return port, nil
		}
	}

	return 0, fmt.Errorf("no available ports in range %d-%d", p.base, p.max)
}

func (p *PortPool) Release(port int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	delete(p.used, port)
}

func (p *PortPool) IsUsed(port int) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return p.used[port]
}

func (p *PortPool) Available() int {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return (p.max - p.base + 1) - len(p.used)
}

func (p *PortPool) Used() []int {
	p.mu.RLock()
	defer p.mu.RUnlock()

	ports := make([]int, 0, len(p.used))
	for port := range p.used {
		ports = append(ports, port)
	}
	return ports
}

func (p *PortPool) Reserve(port int) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if port < p.base || port > p.max {
		return fmt.Errorf("port %d is out of range %d-%d", port, p.base, p.max)
	}

	if p.used[port] {
		return fmt.Errorf("port %d is already in use", port)
	}

	p.used[port] = true
	return nil
}
