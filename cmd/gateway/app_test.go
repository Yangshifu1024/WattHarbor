package main

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	gatewayruntime "github.com/ChargePilot2026/charge-pilot/internal/gateway/runtime"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
)

type idleSink struct{}

func (idleSink) Register(context.Context, protocol.Registration) error { return nil }
func (idleSink) Record(context.Context, protocol.Event) error          { return nil }

type idleAdapter struct{}

type idleStops struct{}

func (idleStops) RetryStopping(context.Context) error { return nil }
func (idleStops) RetryPending(context.Context) error  { return nil }

func (idleAdapter) Name() string                                             { return "test" }
func (idleAdapter) ServeConn(context.Context, net.Conn, protocol.Sink) error { return nil }

func TestTCPBindFailureStopsHTTPAndRetryLoop(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	app := application{
		Server:  &http.Server{Addr: "127.0.0.1:0"},
		Devices: gatewayruntime.Runtime{Endpoints: []protocol.Endpoint{{Address: l.Addr().String(), Adapter: idleAdapter{}}}, Sink: idleSink{}, MaxConnections: 1, Compensation: idleStops{}, UserStops: idleStops{}},
	}
	finished := make(chan error, 1)
	go func() { finished <- app.Run(context.Background()) }()
	select {
	case err := <-finished:
		if err == nil || !strings.Contains(err.Error(), "listen ") {
			t.Fatalf("TCP bind error lost: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP or retry loop kept running after TCP failure")
	}
}
