package page

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/jfyne/live"
)

// RegisterHandler the first part of the component lifecycle, this is called during component creation
// and is used to register any events that the component handles.
type RegisterHandler func(c *Component) error

// MountHandler the components mount function called on first GET request and again when the socket connects.
type MountHandler func(ctx context.Context, c *Component) error

// RenderHandler ths component.
type RenderHandler func(w io.Writer, c *Component) error

// ComponentConstructor a func for creating a new component.
type ComponentConstructor func(ctx context.Context, h live.Handler, s live.Socket) (*Component, error)

var _ live.Child = &Component{}

// Component is a self-contained component on the page. Components can be reused across the application
// or used to compose complex interfaces by splitting events handlers and render logic into
// smaller pieces.
//
// Remember to use a unique ID and use the Event function which scopes the event-name
// to trigger the event in the right component.
type Component struct {
	// id identifies the component on the page. This should be something stable, so that during the mount
	// it can be found again by the socket.
	// When reusing the same component this id should be unique to avoid conflicts.
	id string

	// Handler a reference to the host handler.
	Handler live.Handler

	// Socket a reference to the socket that this component
	// is scoped too.
	Socket live.Socket

	// Register the component. This should be used to setup event handling.
	Register RegisterHandler

	// Mount the component, this should be used to setup the components initial state.
	Mount MountHandler

	// Render the component, this should be used to describe how to render the component.
	Render RenderHandler

	// State the components state.
	State interface{}

	// Any uploads.
	Uploads live.UploadContext

	// eventHandlers the map of client event handlers for this component.
	eventHandlers map[string]live.EventHandler
	// selfHandlers the map of handler event handlers for this component.
	selfHandlers map[string]live.SelfHandler
}

// NewComponent creates a new component and returns it. It does not register it or mount it.
func NewComponent(ID string, h live.Handler, s live.Socket, configurations ...ComponentConfig) (*Component, error) {
	c := &Component{
		id:       ID,
		Handler:  h,
		Socket:   s,
		Register: defaultRegister,
		Mount:    defaultMount,
		Render:   defaultRender,

		eventHandlers: make(map[string]live.EventHandler),
		selfHandlers:  make(map[string]live.SelfHandler),
	}
	for _, conf := range configurations {
		if err := conf(c); err != nil {
			return &Component{}, err
		}
	}

	s.AttachChild(c)

	return c, nil
}

// Init takes a constructor and then registers and mounts the component.
func Init(ctx context.Context, construct func() (*Component, error)) (*Component, error) {
	comp, err := construct()
	if err != nil {
		return nil, fmt.Errorf("could not install component on construct: %w", err)
	}
	if err := comp.Register(comp); err != nil {
		return nil, fmt.Errorf("could not install component on register: %w", err)
	}
	if err := comp.Mount(ctx, comp); err != nil {
		return nil, fmt.Errorf("could not install component on mount: %w", err)
	}
	return comp, nil
}

// ID returns the components ID.
func (c *Component) ID() string {
	return c.id
}

// Self sends an event to this component.
func (c *Component) Self(ctx context.Context, s live.Socket, event string, data interface{}) error {
	// return s.Self(ctx, event, data)
	return nil
}

// HandleSelf handles scoped incoming events from the server.
func (c *Component) HandleSelf(event string, handler live.SelfHandler) {
	c.selfHandlers[event] = handler
}

// HandleEvent handles a component event sent from the client.
func (c *Component) HandleEvent(event string, handler live.EventHandler) {
	c.eventHandlers[event] = handler
}

// HandleParams handles parameter changes. Caution these handlers are not scoped to a specific Component.
func (c *Component) HandleParams(handler live.EventHandler) {
	c.Handler.HandleParams(func(ctx context.Context, s live.Socket, p live.Params) (interface{}, error) {
		state, err := handler(ctx, s, p)
		if err != nil {
			return s.Assigns(), err
		}
		c.State = state
		return s.Assigns(), nil
	})
}

// Event scopes an event string so that it applies only to a Component with the same ID.
func (c *Component) Event(event string) string {
	return c.id + "--" + event
}

func (c *Component) CallSelf(ctx context.Context, event string, s live.Socket, data live.Event) error {
	handler := c.selfHandlers[event]
	if handler == nil {
		return fmt.Errorf("no self handler on component %s for %s: %w", c.ID, event, live.ErrNoEventHandler)
	}
	state, err := handler(ctx, c.Socket, data.SelfData)
	if err != nil {
		return err
	}
	c.State = state
	return nil
}

func (c *Component) CallEvent(ctx context.Context, event string, s live.Socket, data live.Params) error {
	handler := c.eventHandlers[event]
	if handler == nil {
		return fmt.Errorf("no event handler on component %s for %s: %w", c.ID, event, live.ErrNoEventHandler)
	}
	state, err := handler(ctx, c.Socket, data)
	if err != nil {
		return err
	}
	c.State = state
	return nil
}

// String renders the component to a string.
func (c *Component) String() string {
	var buf bytes.Buffer
	if err := c.Render(&buf, c); err != nil {
		return fmt.Sprintf("template rendering failed: %s", err)
	}
	return buf.String()
}

// defaultRegister is the default register handler which does nothing.
func defaultRegister(c *Component) error {
	return nil
}

// defaultMount is the default mount handler which does nothing.
func defaultMount(ctx context.Context, c *Component) error {
	return nil
}

// defaultRender is the default render handler which does nothing.
func defaultRender(w io.Writer, c *Component) error {
	_, err := w.Write([]byte(fmt.Sprintf("%+v", c.State)))
	return err
}

var _ RegisterHandler = defaultRegister
var _ MountHandler = defaultMount
var _ RenderHandler = defaultRender
