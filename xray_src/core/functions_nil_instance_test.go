package core

import (
	"context"
	"testing"

	"github.com/xtls/xray-core/common"
)

type auditContextConfig struct{}

func init() {
	common.Must(common.RegisterConfig((*auditContextConfig)(nil), func(ctx context.Context, _ interface{}) (interface{}, error) {
		return ctx, nil
	}))
}

func TestCreateObjectInstanceContext(t *testing.T) {
	t.Run("nil instance uses background", func(t *testing.T) {
		object, err := CreateObject(nil, &auditContextConfig{})
		if err != nil {
			t.Fatal(err)
		}
		ctx := object.(context.Context)
		if ctx == nil || FromContext(ctx) != nil || ctx.Err() != nil {
			t.Fatalf("unexpected factory context: %v", ctx)
		}
	})

	t.Run("instance retains parent values", func(t *testing.T) {
		type parentKey struct{}
		parent, cancel := context.WithCancel(context.WithValue(context.Background(), parentKey{}, "retained"))
		defer cancel()
		instance := &Instance{ctx: parent}
		object, err := CreateObject(instance, &auditContextConfig{})
		if err != nil {
			t.Fatal(err)
		}
		ctx := object.(context.Context)
		if FromContext(ctx) != instance || ctx.Value(parentKey{}) != "retained" {
			t.Fatal("factory lost instance or parent context value")
		}
		cancel()
		if ctx.Err() != context.Canceled {
			t.Fatal("factory context lost parent cancellation")
		}
	})
}
