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
type RegisterHandler[T any] func(c *Component[T]) error

// MountHandler the components mount function called on first GET request and again when the socket connects.
type MountHandler[T any] func(ctx context.Context, c *Component[T]) error

// RenderHandler ths component.
type RenderHandler[T any] func(w io.Writer, c *Component[T]) error

// ComponentConstructor a func for creating a new component.
type ComponentConstructor[T any] func(ctx context.Context, h live.Handler, s live.Socket) (*Component[T], error)

var _ live.Child = &Component[any]{}

// Component is a self-contained component on the page. Components can be reused across the application
// or used to compose complex interfaces by splitting events handlers and render logic into
// smaller pieces.
//
// Remember to use a unique ID and use the Event function which scopes the event-name
// to trigger the event in the right component.
type Component[T any] struct {
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
	Register RegisterHandler[T]

	// Mount the component, this should be used to setup the components initial state.
	Mount MountHandler[T]

	// Render the component, this should be used to describe how to render the component.
	Render RenderHandler[T]

	// State the components state.
	State T

	// Any uploads.
	Uploads live.UploadContext

	// eventHandlers the map of client event handlers for this component.
	eventHandlers map[string]live.EventHandler[T]
	// selfHandlers the map of handler event handlers for this component.
	selfHandlers map[string]live.SelfHandler[T]
}

// NewComponent creates a new component and returns it. It does not register it or mount it.
func NewComponent[T any](ID string, h live.Handler, s live.Socket, configurations ...ComponentConfig[T]) (*Component[T], error) {
	c := &Component[T]{
		id:       ID,
		Handler:  h,
		Socket:   s,
		Register: defaultRegister[T],
		Mount:    defaultMount[T],
		Render:   defaultRender[T],

		eventHandlers: make(map[string]live.EventHandler[T]),
		selfHandlers:  make(map[string]live.SelfHandler[T]),
	}
	for _, conf := range configurations {
		if err := conf(c); err != nil {
			return &Component[T]{}, err
		}
	}

	s.AttachChild(c)

	return c, nil
}

// Init takes a constructor and then registers and mounts the component.
func Init[T any](ctx context.Context, construct func() (*Component[T], error)) (*Component[T], error) {
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

func (c *Component[T]) GetState() any {
	return c.State
}

// ID returns the components ID.
func (c *Component[T]) ID() string {
	return c.id
}

// Self sends an event to this component.
func (c *Component) Self(ctx context.Context, s live.Socket, event string, data interface{}) error {
	// return s.Self(ctx, event, data)
	return nil
}

// HandleSelf handles scoped incoming events from the server.
func (c *Component[T]) HandleSelf(event string, handler live.SelfHandler[T]) {
	c.selfHandlers[event] = handler
	c.selfHandlers[c.Event(event)] = handler
}

// HandleEvent handles a component event sent from the client.
func (c *Component[T]) HandleEvent(event string, handler live.EventHandler[T]) {
	c.eventHandlers[event] = handler
}

// HandleParams handles parameter changes. Caution these handlers are not scoped to a specific Component.
func (c *Component[T]) HandleParams(handler live.EventHandler[T]) {
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
func (c *Component[T]) Event(event string) string {
	return c.id + "--" + event
}

func (c *Component[T]) CallSelf(ctx context.Context, event string, s live.Socket, data live.Event) error {
	handler := c.selfHandlers[event]
	if handler == nil {
		return fmt.Errorf("no self handler on component %q for %q: %w", c.id, event, live.ErrNoEventHandler)
	}
	state, err := handler(ctx, c.Socket, data.SelfData)
	if err != nil {
		return err
	}
	c.State = state
	return nil
}

func (c *Component[T]) CallEvent(ctx context.Context, event string, s live.Socket, data live.Params) error {
	handler := c.eventHandlers[event]
	if handler == nil {
		return fmt.Errorf("no event handler on component %q for %q: %w", c.id, event, live.ErrNoEventHandler)
	}
	state, err := handler(ctx, c.Socket, data)
	if err != nil {
		return err
	}
	c.State = state
	return nil
}

// String renders the component to a string.
func (c *Component[T]) String() string {
	var buf bytes.Buffer
	if err := c.Render(&buf, c); err != nil {
		return fmt.Sprintf("template rendering failed: %s", err)
	}
	return buf.String()
}

// defaultRegister is the default register handler which does nothing.
func defaultRegister[T any](c *Component[T]) error {
	return nil
}

// defaultMount is the default mount handler which does nothing.
func defaultMount[T any](ctx context.Context, c *Component[T]) error {
	return nil
}

// defaultRender is the default render handler which does nothing.
func defaultRender[T any](w io.Writer, c *Component[T]) error {
	_, err := w.Write([]byte(fmt.Sprintf("%+v", c.State)))
	return err
}

var _ RegisterHandler[any] = defaultRegister[any]
var _ MountHandler[any] = defaultMount[any]
var _ RenderHandler[any] = defaultRender[any]
