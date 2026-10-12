//go:build !linux || !mipsle || !gc || purego

package poly1305

func updateGeneric(state *macState, msg []byte) { updateGenericGo(state, msg) }
