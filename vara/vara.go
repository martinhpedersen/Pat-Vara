package vara

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strings"

	"github.com/imdario/mergo"
	"github.com/la5nta/wl2k-go/transport"
)

const network = "vara"

var errNotImplemented = errors.New("not implemented")

// ModemConfig defines configuration options for connecting with the VARA modem program.
type ModemConfig struct {
	// Host on the network which is hosting VARA; defaults to `localhost`
	Host string
	// CmdPort is the TCP port on which to reach VARA; defaults to 8300
	CmdPort int
	// DataPort is the TCP port on which to exchange over-the-air payloads with VARA;
	// defaults to 8301
	DataPort int
}

var defaultConfig = ModemConfig{
	Host:     "localhost",
	CmdPort:  8300,
	DataPort: 8301,
}

type Modem struct {
	myCall        string
	config        ModemConfig
	cmdConn       *net.TCPConn
	dataConn      *net.TCPConn
	toCall        string
	busy          bool
	connectChange chan connectedState
	lastState     connectedState
	rig           transport.PTTController
}

type connectedState int

const (
	connected connectedState = iota
	disconnected
)

var debug bool

func init() {
	debug = os.Getenv("VARA_DEBUG") != ""
}

// NewModem initializes configuration for a new VARA modem client stub.
func NewModem(myCall string, config ModemConfig) (*Modem, error) {
	// Back-fill empty config values with defaults
	if err := mergo.Merge(&config, defaultConfig); err != nil {
		return nil, err
	}
	return &Modem{
		myCall:        myCall,
		config:        config,
		busy:          false,
		connectChange: make(chan connectedState, 1),
		lastState:     disconnected,
	}, nil
}

// Start establishes TCP connections with the VARA modem program. This must be called before
// sending commands to the modem.
func (m *Modem) start() error {
	// Open command port TCP connection
	var err error
	m.cmdConn, err = m.connectTCP("command", m.config.CmdPort)
	if err != nil {
		return err
	}

	// Start listening for incoming VARA commands
	go m.cmdListen()
	return nil
}

// Close closes the RF and then the TCP connections to the VARA modem. Blocks until finished.
func (m *Modem) Close() error {
	// Send ABORT command
	if m.cmdConn != nil {
		if err := m.writeCmd("ABORT"); err != nil {
			return err
		}
	}

	// Block until VARA modem acks disconnect
	if m.lastState == connected {
		if <-m.connectChange != disconnected {
			return errors.New("disconnect failed")
		}
	}

	// Make sure to stop TX (should have already happened, but this is a backup)
	if m.rig != nil {
		_ = m.rig.SetPTT(false)
	}

	// Clear up internal state
	m.toCall = ""
	return nil
}

func (m *Modem) connectTCP(name string, port int) (*net.TCPConn, error) {
	debugPrint(fmt.Sprintf("Connecting %s", name))
	cmdAddr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf("%s:%d", m.config.Host, port))
	if err != nil {
		return nil, fmt.Errorf("couldn't resolve VARA %s address: %w", name, err)
	}
	conn, err := net.DialTCP("tcp", nil, cmdAddr)
	if err != nil {
		return nil, fmt.Errorf("couldn't connect to VARA %s port: %w", name, err)
	}
	return conn, nil
}

func disconnectTCP(name string, port *net.TCPConn) *net.TCPConn {
	if port == nil {
		return nil
	}
	_ = port.Close()
	debugPrint(fmt.Sprintf("disonnected %s", name))
	return nil
}

// wrapper around m.cmdConn.Write
func (m *Modem) writeCmd(cmd string) error {
	debugPrint(fmt.Sprintf("writing cmd: %v", cmd))
	_, err := m.cmdConn.Write([]byte(cmd + "\r"))
	return err
}

// goroutine listening for incoming commands
func (m *Modem) cmdListen() {
	var buf = make([]byte, 1<<16)
	for {
		if m.cmdConn == nil {
			// probably disconnected
			return
		}
		l, err := m.cmdConn.Read(buf)
		if err != nil {
			debugPrint(fmt.Sprintf("cmdListen err: %v", err))
			continue
		}
		cmds := strings.Split(string(buf[:l]), "\r")
		for _, c := range cmds {
			if c == "" {
				continue
			}
			debugPrint(fmt.Sprintf("got cmd: %v", c))
			switch c {
			case "PTT ON":
				// VARA wants to start TX; send that to the PTTController
				if m.rig != nil {
					_ = m.rig.SetPTT(true)
				}
			case "PTT OFF":
				// VARA wants to stop TX; send that to the PTTController
				if m.rig != nil {
					_ = m.rig.SetPTT(false)
				}
			case "BUSY ON":
				m.busy = true
			case "BUSY OFF":
				m.busy = false
			case "OK":
				// nothing to do
			case "IAMALIVE":
				// nothing to do
			case "DISCONNECTED":
				m.handleDisconnect()
				return
			default:
				if strings.HasPrefix(c, "CONNECTED") {
					m.handleConnect()
					break
				}
				if strings.HasPrefix(c, "BUFFER") {
					// nothing to do
					break
				}
				log.Printf("got a vara command I wasn't expecting: %v", c)
			}
		}
	}
}

func (m *Modem) handleConnect() {
	m.lastState = connected
	m.connectChange <- connected
}

func (m *Modem) handleDisconnect() {
	m.lastState = disconnected
	m.connectChange <- disconnected

	// Close data port TCP connection
	m.dataConn = disconnectTCP("data", m.dataConn)
	// Close command port TCP connection
	m.cmdConn = disconnectTCP("cmd", m.cmdConn)
}

// If env var VARA_DEBUG exists, log more stuff
func debugPrint(msg string) {
	if debug {
		log.Print(msg)
	}
}