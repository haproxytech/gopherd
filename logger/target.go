package logger

import (
	"fmt"
	"io"
	"log/syslog"
	"net/url"
)

// TargetConfig defines a log forwarding target.
type TargetConfig struct {
	Labels   map[string]string   // custom metadata
	Type     string                // "syslog"
	Location string            // e.g. "udp://logs.example.com:514"
	Services []string          // filter: only these service names
}

// Target wraps a log forwarding destination.
type Target struct {
	Writer   io.WriteCloser
	services map[string]bool
	name     string
	cfg      TargetConfig
}

// NewTarget creates a new log target from config.
func NewTarget(name string, cfg TargetConfig) (*Target, error) {
	svcSet := make(map[string]bool)
	for _, s := range cfg.Services {
		svcSet[s] = true
	}

	lt := &Target{
		name:     name,
		cfg:      cfg,
		services: svcSet,
	}

	switch cfg.Type {
	case "syslog":
		w, err := openSyslog(cfg.Location)
		if err != nil {
			return nil, fmt.Errorf("log-target %s: %w", name, err)
		}
		lt.Writer = w
	default:
		return nil, fmt.Errorf("log-target %s: unsupported type %q (supported: syslog)", name, cfg.Type)
	}

	return lt, nil
}

// AppliesTo returns whether this target should receive logs from the given service.
func (lt *Target) AppliesTo(serviceName string) bool {
	if len(lt.services) == 0 {
		return true // no filter = all services
	}
	return lt.services[serviceName]
}

// Close closes the target writer.
func (lt *Target) Close() {
	if lt.Writer != nil {
		lt.Writer.Close()
	}
}

// syslogWriter wraps syslog.Writer to implement io.WriteCloser with line-level writes.
type syslogWriter struct {
	w *syslog.Writer
}

func openSyslog(location string) (io.WriteCloser, error) {
	u, err := url.Parse(location)
	if err != nil {
		return nil, fmt.Errorf("parse syslog location %q: %w", location, err)
	}

	network := u.Scheme // "udp" or "tcp"
	addr := u.Host

	if network == "" || addr == "" {
		return nil, fmt.Errorf("syslog location must be like udp://host:port or tcp://host:port, got %q", location)
	}

	w, err := syslog.Dial(network, addr, syslog.LOG_INFO|syslog.LOG_DAEMON, "gopherd")
	if err != nil {
		return nil, fmt.Errorf("dial syslog %s://%s: %w", network, addr, err)
	}

	return &syslogWriter{w: w}, nil
}

func (sw *syslogWriter) Write(p []byte) (int, error) {
	return len(p), sw.w.Info(string(p))
}

func (sw *syslogWriter) Close() error {
	return sw.w.Close()
}
