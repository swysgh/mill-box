package option

import "github.com/sagernet/sing/common/json/badoption"

type AnoissInboundOptions struct {
	ListenOptions
	Users            []AnoissUser               `json:"users,omitempty"`
	PaddingScheme    badoption.Listable[string] `json:"padding_scheme,omitempty"`
	ServerPrivateKey string                     `json:"server_private_key,omitempty"`
	ClientPublicKeys badoption.Listable[string] `json:"client_public_keys,omitempty"`
	AllowAnyClient   bool                       `json:"allow_any_client,omitempty"`
	PreSharedKey     string                     `json:"pre_shared_key,omitempty"`
	HandshakeTimeout badoption.Duration         `json:"handshake_timeout,omitempty"`
	HandshakePadding *int                       `json:"handshake_padding,omitempty"`
}

type AnoissUser struct {
	Name     string `json:"name,omitempty"`
	Password string `json:"password,omitempty"`
}

type AnoissOutboundOptions struct {
	DialerOptions
	ServerOptions
	Password                 string             `json:"password,omitempty"`
	ClientPrivateKey         string             `json:"client_private_key,omitempty"`
	ServerPublicKey          string             `json:"server_public_key,omitempty"`
	PreSharedKey             string             `json:"pre_shared_key,omitempty"`
	HandshakeTimeout         badoption.Duration `json:"handshake_timeout,omitempty"`
	HandshakePadding         *int               `json:"handshake_padding,omitempty"`
	IdleSessionCheckInterval badoption.Duration `json:"idle_session_check_interval,omitempty"`
	IdleSessionTimeout       badoption.Duration `json:"idle_session_timeout,omitempty"`
	MinIdleSession           int                `json:"min_idle_session,omitempty"`
	ClientMetadata           string             `json:"client_metadata,omitempty"`
}
