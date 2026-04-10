// Package control provides a lightweight TCP control server that enables
// inter-instance communication during migration.
//
// The source instance connects to the successor's control server to:
//  1. Signal the successor to start a CRIU page server.
//  2. Query the successor's readiness state.
//  3. Deliver the page-server address back to the source.
//
// Protocol: newline-delimited JSON over a plain TCP socket on the control port.
// This is intentionally minimal — no TLS needed since both instances are on
// the same private subnet and the connection is short-lived.
package control

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/bons/bons-ci/plugins/oracle/internal/criu"
)

// Command is a request from the source instance.
type Command struct {
	Action         string `json:"action"`
	PageServerPort int    `json:"page_server_port,omitempty"`
	ImageDir       string `json:"image_dir,omitempty"`
}

// Response is the reply from the control server.
type Response struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	// PageServerAddr is returned after a successful "start-page-server" command.
	PageServerAddr string `json:"page_server_addr,omitempty"`
}

// Server listens on a TCP port for control commands.
type Server struct {
	port     int
	criuBin  string
	imageDir string
	log      *zap.Logger
	stopPS   func() // stops the page server if started
}

// NewServer constructs a control Server.
func NewServer(port int, criuBin, imageDir string, log *zap.Logger) *Server {
	return &Server{
		port:     port,
		criuBin:  criuBin,
		imageDir: imageDir,
		log:      log,
	}
}

// ListenAndServe starts the control server and blocks until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(s.port))
	if err != nil {
		return fmt.Errorf("control server listen :%d: %w", s.port, err)
	}
	defer ln.Close()

	s.log.Info("control server listening", zap.Int("port", s.port))

	go func() {
		<-ctx.Done()
		ln.Close()
		if s.stopPS != nil {
			s.stopPS()
		}
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				s.log.Warn("control server accept error", zap.Error(err))
				continue
			}
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second)) //nolint:errcheck

	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		return
	}

	var cmd Command
	if err := json.Unmarshal(scanner.Bytes(), &cmd); err != nil {
		s.writeResponse(conn, Response{OK: false, Message: "invalid JSON"})
		return
	}

	s.log.Info("control command received", zap.String("action", cmd.Action))

	switch cmd.Action {
	case "start-page-server":
		resp := s.handleStartPageServer(ctx, cmd)
		s.writeResponse(conn, resp)

	case "ping":
		s.writeResponse(conn, Response{OK: true, Message: "pong"})

	case "stop-page-server":
		if s.stopPS != nil {
			s.stopPS()
			s.stopPS = nil
		}
		s.writeResponse(conn, Response{OK: true, Message: "page server stopped"})

	default:
		s.writeResponse(conn, Response{OK: false, Message: "unknown action: " + cmd.Action})
	}
}

func (s *Server) handleStartPageServer(ctx context.Context, cmd Command) Response {
	port := cmd.PageServerPort
	if port == 0 {
		port = 27182
	}
	imageDir := cmd.ImageDir
	if imageDir == "" {
		imageDir = s.imageDir
	}

	if err := os.MkdirAll(imageDir, 0o700); err != nil {
		return Response{OK: false, Message: "mkdir: " + err.Error()}
	}

	stop, err := criu.StartPageServer(ctx, s.criuBin, imageDir, port)
	if err != nil {
		s.log.Error("failed to start page server", zap.Error(err))
		return Response{OK: false, Message: "page server failed: " + err.Error()}
	}
	s.stopPS = stop

	// Discover our private IP to return a routable address to the source.
	ip := discoverPrivateIP()
	addr := fmt.Sprintf("%s:%d", ip, port)

	s.log.Info("page server started",
		zap.String("addr", addr),
		zap.String("image_dir", imageDir),
	)
	return Response{OK: true, PageServerAddr: addr}
}

func (s *Server) writeResponse(conn net.Conn, resp Response) {
	data, _ := json.Marshal(resp)
	data = append(data, '\n')
	conn.Write(data) //nolint:errcheck
}

// ────────────────────────────────────────────────────────────────────────────

// SendCommand sends a command to the successor's control server and returns the response.
func SendCommand(ctx context.Context, addr string, cmd Command) (*Response, error) {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connecting to control server %s: %w", addr, err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second)) //nolint:errcheck

	data, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		return nil, fmt.Errorf("sending command: %w", err)
	}

	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		return nil, fmt.Errorf("no response from control server")
	}

	var resp Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}
	return &resp, nil
}

// WaitForControlServer polls addr until the server responds to a ping
// or ctx is cancelled.  Used by the source to wait for the successor
// to finish booting.
func WaitForControlServer(ctx context.Context, addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		resp, err := SendCommand(ctx, addr, Command{Action: "ping"})
		if err == nil && resp.OK {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("control server at %s not ready after %s", addr, timeout)
}

// discoverPrivateIP returns the primary non-loopback IP of this host.
func discoverPrivateIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		if ip4 := ipNet.IP.To4(); ip4 != nil {
			return ip4.String()
		}
	}
	return "127.0.0.1"
}
