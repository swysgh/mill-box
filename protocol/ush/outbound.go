package ush

import (
	"bytes"
	"context"
	"encoding/base64"
	"math/rand"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/swysgh/mill-box/adapter"
	"github.com/swysgh/mill-box/adapter/outbound"
	"github.com/swysgh/mill-box/common/dialer"
	C "github.com/swysgh/mill-box/constant"
	"github.com/swysgh/mill-box/log"
	"github.com/swysgh/mill-box/option"

	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/uot"
	"github.com/sagernet/sing/service/filemanager"

	"golang.org/x/crypto/ssh"
)

func RegisterOutbound(registry *outbound.Registry) {
	outbound.Register[option.USHOutboundOptions](registry, C.TypeUSH, NewOutbound)
}

var (
	_ adapter.InterfaceUpdateListener = (*Outbound)(nil)
	_ adapter.IdleConnectionKeeper    = (*Outbound)(nil)
	_ adapter.OutboundWithMultiplex   = (*Outbound)(nil)
)

// Outbound carries TCP over ordinary SSH direct-tcpip channels and UDP over UoT
// streams opened on the same SSH connection, so a single sshd port serves both.
type Outbound struct {
	outbound.Adapter
	logger            logger.ContextLogger
	dialer            N.Dialer
	serverAddr        M.Socksaddr
	user              string
	hostKey           []ssh.PublicKey
	hostKeyAlgorithms []string
	cipher            []string
	mac               []string
	kexAlgorithm      []string
	clientVersion     string
	authMethod        []ssh.AuthMethod
	uotServerAddr     M.Socksaddr
	uotClient         *uot.Client
	clientAccess      sync.Mutex
	clientConn        net.Conn
	client            *ssh.Client
	streams           int
	closeIdle         bool
}

func NewOutbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.USHOutboundOptions) (adapter.Outbound, error) {
	// mill-box check only runs constructors, so every fatal misconfiguration has
	// to be rejected here rather than in Start.
	if options.Server == "" {
		return nil, E.New("ush: server is required")
	}
	if options.UoTServer == "" {
		return nil, E.New("ush: uot_server is required")
	}
	uotServerAddr := M.ParseSocksaddr(options.UoTServer)
	if !uotServerAddr.IsValid() || uotServerAddr.Port == 0 {
		return nil, E.New("ush: invalid uot_server ", options.UoTServer, ": host and port are required")
	}
	switch options.UoTVersion {
	case 0, uot.Version:
	default:
		// Legacy UoT (version 1) sends no request header at all: the server can
		// only recognise it from the magic address the client connected to,
		// which a fixed-port ush inbound cannot observe.
		return nil, E.New("ush: unsupported uot_version ", options.UoTVersion, ", only ", uot.Version, " is supported")
	}
	outboundDialer, err := dialer.New(ctx, options.DialerOptions, options.ServerIsDomain())
	if err != nil {
		return nil, err
	}
	outbound := &Outbound{
		Adapter:           outbound.NewAdapterWithDialerOptions(C.TypeUSH, tag, []string{N.NetworkTCP, N.NetworkUDP}, options.DialerOptions),
		logger:            logger,
		dialer:            outboundDialer,
		serverAddr:        options.ServerOptions.Build(),
		user:              options.User,
		hostKeyAlgorithms: options.HostKeyAlgorithms,
		cipher:            options.Cipher,
		mac:               options.MAC,
		kexAlgorithm:      options.KexAlgorithm,
		clientVersion:     options.ClientVersion,
		uotServerAddr:     uotServerAddr,
	}
	if outbound.serverAddr.Port == 0 {
		outbound.serverAddr.Port = 22
	}
	if outbound.user == "" {
		outbound.user = "root"
	}
	if outbound.clientVersion == "" {
		outbound.clientVersion = randomVersion()
	}
	if options.Password != "" {
		outbound.authMethod = append(outbound.authMethod, ssh.Password(options.Password))
	}
	if len(options.PrivateKey) > 0 || options.PrivateKeyPath != "" {
		var privateKey []byte
		if len(options.PrivateKey) > 0 {
			privateKey = []byte(strings.Join(options.PrivateKey, "\n"))
		} else {
			var err error
			privateKey, err = filemanager.ReadFile(ctx, os.ExpandEnv(options.PrivateKeyPath))
			if err != nil {
				return nil, E.Cause(err, "read private key")
			}
		}
		var signer ssh.Signer
		var err error
		if options.PrivateKeyPassphrase == "" {
			signer, err = ssh.ParsePrivateKey(privateKey)
		} else {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(privateKey, []byte(options.PrivateKeyPassphrase))
		}
		if err != nil {
			return nil, E.Cause(err, "parse private key")
		}
		outbound.authMethod = append(outbound.authMethod, ssh.PublicKeys(signer))
	}
	if len(options.HostKey) > 0 {
		for _, hostKey := range options.HostKey {
			key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(hostKey))
			if err != nil {
				return nil, E.Cause(err, "parse host key: ", hostKey)
			}
			outbound.hostKey = append(outbound.hostKey, key)
		}
	}
	if len(outbound.authMethod) == 0 {
		return nil, E.New("ush: no authentication method configured, set password or private_key/private_key_path")
	}
	outbound.uotClient = &uot.Client{
		Dialer:  &uotServerDialer{outbound: outbound},
		Version: options.UoTVersion,
	}
	return outbound, nil
}

func randomVersion() string {
	version := "SSH-2.0-OpenSSH_"
	if rand.Intn(2) == 0 {
		version += "7." + strconv.Itoa(rand.Intn(10))
	} else {
		version += "8." + strconv.Itoa(rand.Intn(9))
	}
	return version
}

func (s *Outbound) connect(ctx context.Context) (client *ssh.Client, err error) {
	s.clientAccess.Lock()
	defer s.clientAccess.Unlock()

	if s.client != nil {
		s.streams++
		return s.client, nil
	}

	conn, err := s.dialer.DialContext(ctx, N.NetworkTCP, s.serverAddr)
	if err != nil {
		return nil, err
	}
	if ctx.Done() != nil {
		handshakeConn := conn
		stopContext := context.AfterFunc(ctx, func() {
			_ = handshakeConn.Close()
		})
		defer func() {
			if !stopContext() {
				s.client = nil
				s.clientConn = nil
				client = nil
				err = ctx.Err()
			}
		}()
	}
	config := &ssh.ClientConfig{
		User:              s.user,
		Auth:              s.authMethod,
		ClientVersion:     s.clientVersion,
		HostKeyAlgorithms: s.hostKeyAlgorithms,
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			if len(s.hostKey) == 0 {
				return nil
			}
			serverKey := key.Marshal()
			for _, hostKey := range s.hostKey {
				if bytes.Equal(serverKey, hostKey.Marshal()) {
					return nil
				}
			}
			return E.New("host key mismatch, server send ", key.Type(), " ", base64.StdEncoding.EncodeToString(serverKey))
		},
	}
	if len(s.cipher) > 0 {
		config.Ciphers = s.cipher
	}
	if len(s.mac) > 0 {
		config.MACs = s.mac
	}
	if len(s.kexAlgorithm) > 0 {
		config.KeyExchanges = s.kexAlgorithm
	}
	clientConn, chans, reqs, err := ssh.NewClientConn(conn, s.serverAddr.Addr.String(), config)
	if err != nil {
		conn.Close()
		return nil, E.Cause(err, "connect to ssh server")
	}

	client = ssh.NewClient(clientConn, chans, reqs)

	s.clientConn = conn
	s.client = client
	s.streams = 1

	go func() {
		client.Wait()
		conn.Close()
		s.clientAccess.Lock()
		if s.client == client {
			s.client = nil
			s.clientConn = nil
		}
		s.clientAccess.Unlock()
	}()

	return client, nil
}

func (s *Outbound) InterfaceUpdated(ctx context.Context) {
	s.clientAccess.Lock()
	clientConn := s.clientConn
	s.clientAccess.Unlock()
	common.Close(clientConn)
}

func (s *Outbound) MultiplexEnabled() bool {
	return true
}

func (s *Outbound) SetKeepIdleConnections(keep bool) {
	s.clientAccess.Lock()
	s.closeIdle = !keep
	s.clientAccess.Unlock()
	if !keep {
		s.CloseIdleConnections()
	}
}

func (s *Outbound) CloseIdleConnections() {
	s.clientAccess.Lock()
	if s.client == nil || s.streams > 0 {
		s.clientAccess.Unlock()
		return
	}
	clientConn := s.clientConn
	s.client = nil
	s.clientConn = nil
	s.clientAccess.Unlock()
	common.Close(clientConn)
}

func (s *Outbound) releaseStream(client *ssh.Client, keepSession bool) {
	s.clientAccess.Lock()
	if s.client != client {
		s.clientAccess.Unlock()
		return
	}
	s.streams--
	if !s.closeIdle || keepSession || s.streams > 0 {
		s.clientAccess.Unlock()
		return
	}
	clientConn := s.clientConn
	s.client = nil
	s.clientConn = nil
	s.clientAccess.Unlock()
	common.Close(clientConn)
}

func (s *Outbound) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	if stage != adapter.StartStateInitialize {
		return nil
	}
	scope.Add(func() error {
		s.clientAccess.Lock()
		clientConn := s.clientConn
		s.clientAccess.Unlock()
		return common.Close(clientConn)
	})
	return nil
}

func (s *Outbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	ctx, metadata := adapter.ExtendContext(ctx)
	metadata.Outbound = s.Tag()
	metadata.Destination = destination
	switch N.NetworkName(network) {
	case N.NetworkTCP:
		s.logger.InfoContext(ctx, "outbound connection to ", destination)
		return s.dialSSH(ctx, destination)
	case N.NetworkUDP:
		s.logger.InfoContext(ctx, "outbound UoT packet connection to ", destination)
		return s.uotClient.DialContext(ctx, N.NetworkUDP, destination)
	default:
		return nil, E.Extend(N.ErrUnknownNetwork, network)
	}
}

func (s *Outbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	ctx, metadata := adapter.ExtendContext(ctx)
	metadata.Outbound = s.Tag()
	metadata.Destination = destination
	s.logger.InfoContext(ctx, "outbound UoT packet connection to ", destination)
	return s.uotClient.ListenPacket(ctx, destination)
}

// dialSSH opens a direct-tcpip channel to destination, i.e. the server side
// sshd makes the outbound TCP connection.
func (s *Outbound) dialSSH(ctx context.Context, destination M.Socksaddr) (net.Conn, error) {
	client, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := client.Dial(N.NetworkTCP, destination.String())
	if err != nil {
		s.releaseStream(client, false)
		return nil, err
	}
	keepSession := adapter.KeepSessionFromContext(ctx)
	return &chanConnWrapper{Conn: conn, onClose: func() { s.releaseStream(client, keepSession) }}, nil
}

// dialUoTServer opens a direct-tcpip channel to the UoT listener on the server
// instead of to a real destination.
func (s *Outbound) dialUoTServer(ctx context.Context) (net.Conn, error) {
	client, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := client.Dial(N.NetworkTCP, s.uotServerAddr.String())
	if err != nil {
		s.releaseStream(client, false)
		return nil, err
	}
	keepSession := adapter.KeepSessionFromContext(ctx)
	return &chanConnWrapper{Conn: conn, onClose: func() { s.releaseStream(client, keepSession) }}, nil
}

// uotServerDialer feeds uot.Client with SSH channels to the server side UoT
// listener. uot.Client ignores the destination it is handed and always dials
// the UoT magic address, so this dialer ignores it too.
type uotServerDialer struct {
	outbound *Outbound
}

func (d *uotServerDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return d.outbound.dialUoTServer(ctx)
}

func (d *uotServerDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, os.ErrInvalid
}

type chanConnWrapper struct {
	net.Conn
	closeOnce sync.Once
	onClose   func()
}

func (c *chanConnWrapper) Close() error {
	err := c.Conn.Close()
	c.closeOnce.Do(c.onClose)
	return err
}

func (c *chanConnWrapper) SetDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (c *chanConnWrapper) SetReadDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (c *chanConnWrapper) SetWriteDeadline(t time.Time) error {
	return os.ErrInvalid
}
