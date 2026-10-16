package option

// USHInboundOptions is the server half of ush: a bare UoT listener. It is meant
// to be reachable only through an SSH tunnel, so it carries no authentication
// fields of its own.
type USHInboundOptions struct {
	ListenOptions
}

// USHOutboundOptions is the client half of ush. TCP is carried by the SSH
// transport exactly like the ssh outbound; UDP is framed as UoT and pushed
// through the same SSH connection to a UoT listener on the server.
type USHOutboundOptions struct {
	SSHOutboundOptions
	UoTServer  string `json:"uot_server"`
	UoTVersion uint8  `json:"uot_version,omitempty"`
}
