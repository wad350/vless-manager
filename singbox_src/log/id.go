package log

import (
	"context"
	"math/rand"
	"time"
)

type (
	idKey    struct{}
	muxIdKey struct{}
	hwidKey  struct{}
)

type ID struct {
	ID        uint32
	CreatedAt time.Time
}

func ContextWithNewID(ctx context.Context) context.Context {
	return ContextWithID(ctx, ID{
		ID:        rand.Uint32(),
		CreatedAt: time.Now(),
	})
}

func ContextWithID(ctx context.Context, id ID) context.Context {
	return context.WithValue(ctx, (*idKey)(nil), id)
}

func IDFromContext(ctx context.Context) (ID, bool) {
	id, loaded := ctx.Value((*idKey)(nil)).(ID)
	return id, loaded
}

func ContextWithNewMuxID(ctx context.Context) context.Context {
	return ContextWithMuxID(ctx, ID{
		ID:        rand.Uint32(),
		CreatedAt: time.Now(),
	})
}

func ContextWithMuxID(ctx context.Context, id ID) context.Context {
	return context.WithValue(ctx, (*muxIdKey)(nil), id)
}

func MuxIDFromContext(ctx context.Context) (ID, bool) {
	id, loaded := ctx.Value((*muxIdKey)(nil)).(ID)
	return id, loaded
}

func ContextWithHWID(ctx context.Context, id ID) context.Context {
	return context.WithValue(ctx, (*hwidKey)(nil), id)
}

func HWIDFromContext(ctx context.Context) (ID, bool) {
	id, loaded := ctx.Value((*hwidKey)(nil)).(ID)
	return id, loaded
}
