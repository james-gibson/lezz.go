//
//  protocol.go
//  lezz.go
//
//  The stdio channel between `lezz sea run` and the lezz.sea app it starts.
//
//  The app is the child, so it can write requests to stdout and read replies
//  from stdin. A pipe is inherited from this process and from nowhere else:
//  no socket, no URL scheme and no file another process can race. lezz.go
//  remains the only side that executes anything, which is what lets the
//  sandboxed build launch a lab.
//
//  One JSON object per line. Anything that is not a protocol message is logged
//  and ignored, so a framework warning on stdout cannot break the channel.
//

package sea

import "encoding/json"

const (
	// ChannelEnv marks the app as channel-launched. Without it lezz.sea is a
	// viewer and prints no protocol lines.
	ChannelEnv = "LEZZ_CHANNEL"
	// ProtocolVersion is the channel's schema version.
	ProtocolVersion = 1
)

// Message is the wire shape in both directions. Type decides which fields are
// meaningful: "hello", "request" or "reply".
type Message struct {
	V    int    `json:"v"`
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`

	// hello
	ClustersRoot string `json:"clustersRoot,omitempty"`
	Version      string `json:"version,omitempty"`
	Build        string `json:"build,omitempty"`

	// request
	Want             string   `json:"want,omitempty"`
	Cluster          string   `json:"cluster,omitempty"`
	Members          []Member `json:"members,omitempty"`
	Slug             string   `json:"slug,omitempty"`
	HealthPorts      []int    `json:"healthPorts,omitempty"`
	GherkinDirectory string   `json:"gherkinDirectory,omitempty"`

	// reply
	OK    *bool  `json:"ok,omitempty"`
	Error string `json:"error,omitempty"`
	ADHD  *ADHD  `json:"adhd,omitempty"`
}

// Member is one alarm the app asks lezz.go to start. Config is the derived
// YAML itself, not a path: the app's sandbox container is protected from other
// processes, so lezz.go cannot read a file the app wrote there.
type Member struct {
	Name   string `json:"name,omitempty"`
	Config string `json:"config,omitempty"`
}

// ADHD is the pairing result: where adhd answers, and the files it owns.
type ADHD struct {
	Endpoint   string `json:"endpoint,omitempty"`
	Port       int    `json:"port,omitempty"`
	ConfigPath string `json:"configPath,omitempty"`
	PIDPath    string `json:"pidPath,omitempty"`
}

// Wants accepted by the supervisor.
const (
	WantStart  = "start"
	WantStop   = "stop"
	WantADHD   = "adhd"
	WantReveal = "reveal"
)

// ReplyOK builds a successful reply.
func ReplyOK(id string) Message {
	ok := true
	return Message{V: ProtocolVersion, Type: "reply", ID: id, OK: &ok}
}

// ReplyError builds a failed reply. The app shows Error to the operator.
func ReplyError(id, message string) Message {
	ok := false
	return Message{V: ProtocolVersion, Type: "reply", ID: id, OK: &ok, Error: message}
}

// Decode parses one protocol line.
func Decode(line []byte) (Message, error) {
	var m Message
	if err := json.Unmarshal(line, &m); err != nil {
		return Message{}, err
	}
	return m, nil
}
