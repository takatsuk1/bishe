package agentmanager

import (
	"fmt"
	"sync"
)

// PortPool 端口池结构体，用于管理可用端口的分配和释放
type PortPool struct {
	base int          // 端口范围起始值
	max  int          // 端口范围结束值
	used map[int]bool // 已使用的端口映射
	mu   sync.RWMutex // 读写锁，保证并发安全
}

// NewPortPool 创建一个新的端口池
// 参数:
//
//	base - 端口范围起始值
//	max - 端口范围结束值
//
// 返回值:
//
//	新创建的端口池实例
func NewPortPool(base, max int) *PortPool {
	return &PortPool{
		base: base,
		max:  max,
		used: make(map[int]bool),
	}
}

// Allocate 从端口池中分配一个可用端口
// 返回值:
//
//	分配的端口号，如果没有可用端口则返回错误
func (p *PortPool) Allocate() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 遍历端口范围，找到第一个可用端口
	for port := p.base; port <= p.max; port++ {
		if !p.used[port] {
			p.used[port] = true
			return port, nil
		}
	}

	return 0, fmt.Errorf("no available ports in range %d-%d", p.base, p.max)
}

// Release 释放端口，使其可以被重新分配
// 参数:
//
//	port - 要释放的端口号
func (p *PortPool) Release(port int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	delete(p.used, port)
}

// IsUsed 检查指定端口是否已被使用
// 参数:
//
//	port - 要检查的端口号
//
// 返回值:
//
//	如果端口已被使用返回true，否则返回false
func (p *PortPool) IsUsed(port int) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return p.used[port]
}

// Available 返回可用端口数量
// 返回值:
//
//	当前可用的端口数量
func (p *PortPool) Available() int {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return (p.max - p.base + 1) - len(p.used)
}

// Used 返回已使用的端口列表
// 返回值:
//
//	已使用的端口号数组
func (p *PortPool) Used() []int {
	p.mu.RLock()
	defer p.mu.RUnlock()

	ports := make([]int, 0, len(p.used))
	for port := range p.used {
		ports = append(ports, port)
	}
	return ports
}

// Reserve 预留指定的端口
// 参数:
//
//	port - 要预留的端口号
//
// 返回值:
//
//	如果预留成功返回nil，否则返回错误
func (p *PortPool) Reserve(port int) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 检查端口是否在有效范围内
	if port < p.base || port > p.max {
		return fmt.Errorf("port %d is out of range %d-%d", port, p.base, p.max)
	}

	// 检查端口是否已被使用
	if p.used[port] {
		return fmt.Errorf("port %d is already in use", port)
	}

	p.used[port] = true
	return nil
}
