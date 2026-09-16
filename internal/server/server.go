// Package server 는 Modbus TCP 리스너와 커넥션 수명 관리를 담당한다.
package server

import (
	"context"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"

	"modbus-slave/internal/config"
	"modbus-slave/internal/modbus"
)

// Server 는 slave 인스턴스 하나다.
type Server struct {
	cfg     *config.Config
	handler *modbus.Handler
	log     *slog.Logger

	ln      net.Listener
	sem     chan struct{}
	wg      sync.WaitGroup
	closing atomic.Bool

	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

// New 는 설정으로부터 서버를 만든다. 아직 포트를 열지는 않는다.
func New(cfg *config.Config, log *slog.Logger) *Server {
	return &Server{
		cfg:     cfg,
		handler: modbus.NewHandler(cfg.Registers, cfg.FunctionCodes),
		log:     log,
		sem:     make(chan struct{}, cfg.MaxConns),
		conns:   make(map[net.Conn]struct{}),
	}
}

// Listen 은 포트를 연다. 바인드 실패를 Serve 이전에 드러내기 위해 분리했다.
func (s *Server) Listen() error {
	addr := net.JoinHostPort(s.cfg.Bind, strconv.Itoa(s.cfg.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.ln = ln
	return nil
}

// Addr 은 실제로 바인드된 주소다. --port 0 으로 띄운 테스트에서 포트를 알아낼 때 쓴다.
func (s *Server) Addr() net.Addr {
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

// Port 는 실제 바인드된 포트다.
func (s *Server) Port() int {
	if a, ok := s.Addr().(*net.TCPAddr); ok {
		return a.Port
	}
	return s.cfg.Port
}

// Serve 는 accept 루프를 돈다. Shutdown 으로 종료되면 nil 을 반환한다.
func (s *Server) Serve() error {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			if s.closing.Load() {
				return nil
			}
			return err
		}
		select {
		case s.sem <- struct{}{}:
		default:
			s.log.Warn("동시 접속 상한 초과로 커넥션 거절",
				"remote", c.RemoteAddr().String(), "max_conns", s.cfg.MaxConns)
			_ = c.Close()
			continue
		}
		s.track(c)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { <-s.sem }()
			defer s.untrack(c)
			s.handleConn(c)
		}()
	}
}

// Shutdown 은 리스너를 닫고 진행 중인 커넥션이 끝나기를 기다린다.
// ctx 가 먼저 끝나면 남은 커넥션을 강제로 닫는다.
func (s *Server) Shutdown(ctx context.Context) error {
	s.closing.Store(true)
	if s.ln != nil {
		_ = s.ln.Close()
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		s.closeAll()
		<-done
		return ctx.Err()
	}
}

func (s *Server) track(c net.Conn) {
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()
}

func (s *Server) untrack(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

func (s *Server) closeAll() {
	s.mu.Lock()
	for c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()
}
