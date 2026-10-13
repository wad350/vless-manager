package crypto

import (
	"crypto/aes"
	"crypto/cipher"

	"github.com/xtls/xray-core/common"
)

// NewAesDecryptionStream creates an AES-CFB decryption stream based on the given key and IV.
// The key must be 16, 24 or 32 bytes, and the IV must be 16 bytes.
func NewAesDecryptionStream(key []byte, iv []byte) cipher.Stream {
	return NewAesStreamMethod(key, iv, cipher.NewCFBDecrypter)
}

// NewAesEncryptionStream creates an AES-CFB encryption stream based on the given key and IV.
// The key must be 16, 24 or 32 bytes, and the IV must be 16 bytes.
func NewAesEncryptionStream(key []byte, iv []byte) cipher.Stream {
	return NewAesStreamMethod(key, iv, cipher.NewCFBEncrypter)
}

func NewAesStreamMethod(key []byte, iv []byte, f func(cipher.Block, []byte) cipher.Stream) cipher.Stream {
	aesBlock, err := aes.NewCipher(key)
	common.Must(err)
	return f(aesBlock, iv)
}

// NewAesCTRStream creates a stream cipher based on AES-CTR.
func NewAesCTRStream(key []byte, iv []byte) cipher.Stream {
	return NewAesStreamMethod(key, iv, cipher.NewCTR)
}

// NewAesGcm creates a AEAD cipher based on AES-GCM.
func NewAesGcm(key []byte) cipher.AEAD {
	block := common.Must2(aes.NewCipher(key))
	aead := common.Must2(cipher.NewGCM(block))
	return aead
}
