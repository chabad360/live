package page

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/jfyne/live"
)

// ComponentConfig configures a component.
type ComponentConfig[T any] func(c *Component[T]) error

// WithRegister set a register handler on the component.
func WithRegister[T any](fn RegisterHandler[T]) ComponentConfig[T] {
	return func(c *Component[T]) error {
		c.Register = fn
		return nil
	}
}

// WithMount set a mount handler on the component.
func WithMount[T any](fn MountHandler[T]) ComponentConfig[T] {
	return func(c *Component[T]) error {
		c.Mount = fn
		return nil
	}
}

// WithRender set a render handler on the component.
func WithRender[T any](fn RenderHandler[T]) ComponentConfig[T] {
	return func(c *Component[T]) error {
		c.Render = fn
		return nil
	}
}

// WithComponentMount set the live.Handler to mount the root component.
func WithComponentMount[T any](construct ComponentConstructor[T]) live.HandlerConfig {
	return func(h live.Handler) error {
		h.HandleMount(func(ctx context.Context, s live.Socket) (interface{}, error) {
			root, err := construct(ctx, h, s)
			if err != nil {
				return nil, fmt.Errorf("failed to construct root component: %w", err)
			}
			if s.Connected() {
				if err := root.Register(root); err != nil {
					return nil, err
				}
			}
			if err := root.Mount(ctx, root); err != nil {
				return nil, err
			}
			return root, nil
		})
		return nil
	}
}

// WithComponentRenderer set the live.Handler to use a root component to render.
func WithComponentRenderer[T any]() live.HandlerConfig {
	return func(h live.Handler) error {
		h.HandleRender(func(_ context.Context, data *live.RenderContext) (io.Reader, error) {
			c, ok := data.Assigns.(*Component[T])
			if !ok {
				return nil, fmt.Errorf("root render data is not a component")
			}
			c.Uploads = data.Uploads
			var buf bytes.Buffer
			if err := c.Render(&buf, c); err != nil {
				return nil, err
			}
			return &buf, nil
		})
		return nil
	}
}
