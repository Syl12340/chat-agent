package channel

import "context"

// Capabilities describe transport behavior; they never grant model tools or
// host permissions. Renderers use this to degrade rich app output safely.
type Capabilities struct {
	Text      bool `json:"text"`
	Images    bool `json:"images"`
	Files     bool `json:"files"`
	Voice     bool `json:"voice"`
	Reply     bool `json:"reply"`
	Group     bool `json:"group"`
	Proactive bool `json:"proactive"`
	Edit      bool `json:"edit"`
	Delete    bool `json:"delete"`
}

type Delivery struct {
	ID               string
	AccountID        string
	ConversationKind string
	TargetID         string
	ReplyTo          string
	Text             string
	Proactive        bool
}

type DeliveryReceipt struct {
	ExternalMessageID string
}

// Adapter is deliberately transport-only. Implementations may authenticate,
// receive and send provider messages, but cannot call a model or memory store.
type Adapter interface {
	Platform() string
	Capabilities() Capabilities
	Run(context.Context, func(context.Context, Envelope) error) error
	Send(context.Context, Delivery) (DeliveryReceipt, error)
}
