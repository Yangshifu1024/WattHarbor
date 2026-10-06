// Command simulator-dc589 运行本地 DC589 设备模拟器，使用实际 5.8.9 协议连接网关。
// 仅用于开发和测试，不随生产服务启动；其他设备协议使用独立命令。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/simulator/dc589"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol/dc589"
)

// 协议板号为 16 位十进制数字；未指定时使用预置测试编号。
const defaultBoardID = "5348240514082652"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "simulator-dc589:", err)
		os.Exit(1)
	}
}

func run() (err error) {
	var (
		gateway    = flag.String("gateway", "127.0.0.1:9100", "gateway TCP address (host:port)")
		boardID    = flag.String("board-id", defaultBoardID, "board id, exactly sixteen decimal digits")
		hardware   = flag.String("hardware", "SH10HP01", "hardware version, eight characters")
		softwareID = flag.String("software-id", "DC589SW1", "software id, eight characters")
		software   = flag.Int("software-version", 107, "software version number")
		module     = flag.String("module", "1234567812345678", "module id, sixteen digits")
		sim        = flag.String("sim", "89860000000000000001", "SIM id, twenty digits")
		strength   = flag.Int("signal", 4, "reported signal strength")
		ports      = flag.Int("ports", 2, "number of charging ports on the board")
		scenario   = flag.String("scenario", string(dc589sim.ScenarioByEnergy), "behaviour: by-energy, by-time, stop-on-command, reject-start, fault, silent, reconnect")
		heartbeat  = flag.Duration("heartbeat", 60*time.Second, "initial idle heartbeat interval; charging uses 15s")
		power      = flag.Uint("power", 1500, "draw while charging, in tenths of a watt")
		reconnect  = flag.Duration("reconnect-after", 3*time.Second, "pause before reconnecting in the reconnect scenario")
		faultAfter = flag.Duration("fault-after", 5*time.Second, "delay before reporting a fault in the fault scenario")
		control    = flag.String("control", "127.0.0.1:9190", "loopback HTTP listener for /state and /events; empty disables it")
		stateFile  = flag.String("state-file", "", "persist configuration, ports, active charges and pending reports in this JSON file")
		tui        = flag.Bool("tui", true, "interactive terminal UI; set false for headless/CI")
	)
	flag.Parse()
	if *ports < 1 || *ports > 20 || *power > 65535 || *software < 0 || *software > 65535 || *strength < 0 || *strength > 255 {
		return errors.New("ports must be 1-20; power/software 0-65535; signal 0-255")
	}

	logger := log.New(os.Stdout, "dc589sim ", log.LstdFlags)
	if *tui {
		logger = dc589sim.NewTUILogger()
	}
	if err := checkBoardID(*boardID); err != nil {
		return err
	}
	choice := dc589sim.Scenario(*scenario)
	if err := checkScenario(choice); err != nil {
		return err
	}

	config := dc589sim.Config{
		Identity: dc589.DeviceIdentity{
			BoardID:         *boardID,
			HardwareVersion: *hardware,
			SoftwareID:      *softwareID,
			SoftwareVersion: uint16(*software),
			ModuleID:        *module,
			SIM:             *sim,
			Signal:          byte(*strength),
		},
		PortCount:      *ports,
		Gateway:        *gateway,
		Scenario:       choice,
		Heartbeat:      *heartbeat,
		PowerDeciWatts: uint32(*power),
		ReconnectAfter: *reconnect,
		FaultAfter:     *faultAfter,
		Log:            logger,
	}

	logger.Printf("registering as %s; the gateway rejects unknown devices", *boardID)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	config.StateFile = *stateFile
	inputs := make(chan dc589sim.Input, 16)
	config.Inputs = inputs
	if *control != "" {
		host, _, err := net.SplitHostPort(*control)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return errors.New("control listener must use a loopback IP")
		}
		listener, err := net.Listen("tcp", *control)
		if err != nil {
			return err
		}
		server := &http.Server{Handler: dc589sim.ControlHandler(ctx, inputs), ReadHeaderTimeout: 5 * time.Second, ErrorLog: logger}
		controlDone := make(chan error, 1)
		defer func() {
			err = errors.Join(err, server.Close(), <-controlDone)
		}()
		go func() {
			err := server.Serve(listener)
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			} else if err != nil {
				logger.Printf("control: %v", err)
				err = fmt.Errorf("control: %w", err)
				stop()
			}
			controlDone <- err
		}()
		logger.Printf("physical controls: http://%s/state and POST /events", *control)
	}
	logger.Printf("connecting to %s, scenario %s", *gateway, choice)
	if *tui {
		return dc589sim.RunTUI(ctx, config, inputs, stop)
	}
	return dc589sim.Run(ctx, config)
}

// checkBoardID 在建立连接前校验板号，拒绝协议帧无法表示的标识符。
func checkBoardID(id string) error {
	if len(id) != dc589.BoardIDDigits {
		return fmt.Errorf("board id %q has %d characters; a 5.8.9 board id is exactly %d decimal digits", id, len(id), dc589.BoardIDDigits)
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return fmt.Errorf("board id %q contains %q; a 5.8.9 board id is decimal digits only", id, r)
		}
	}
	return nil
}

func checkScenario(s dc589sim.Scenario) error {
	known := map[dc589sim.Scenario]bool{
		dc589sim.ScenarioByEnergy:      true,
		dc589sim.ScenarioByTime:        true,
		dc589sim.ScenarioStopOnCommand: true,
		dc589sim.ScenarioRejectStart:   true,
		dc589sim.ScenarioFault:         true,
		dc589sim.ScenarioSilent:        true,
		dc589sim.ScenarioReconnect:     true,
	}
	if !known[s] {
		names := make([]string, 0, len(known))
		for name := range known {
			names = append(names, string(name))
		}
		return fmt.Errorf("unknown scenario %q; choose one of %s", s, strings.Join(names, ", "))
	}
	return nil
}
