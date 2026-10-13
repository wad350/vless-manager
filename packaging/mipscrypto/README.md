# MIPS crypto

Linux MIPSLE-сборка добавляет MIPS32r2 backend ChaCha20 и отдельно включаемый
Poly1305. Публичные API, алгоритмы, ключи, nonce, теги и проверки upstream
сохраняются. ARM64 не меняется. Ускорение шифра не гарантирует рост скорости VPN.

```sh
go test ./packaging/mipscrypto
make ipk ARCH=mipsel-3.4 GOARCH=mipsle GOMIPS=softfloat
make manager GOARCH=mipsle MIPS_CRYPTO=0
```

Генератор не изменяет `GOMODCACHE` или корневой `go.mod`. Он проверяет закреплённую
версию и исходники x/crypto, создаёт копию и alternate modfile в
`build/mips-crypto/`. Кеш зависит от полного содержимого upstream, overlay и
генератора; перед повторным использованием проверяется весь staged-модуль.
Публикация атомарная, записи сериализованы файловой блокировкой. Повреждённый
кеш вызывает ошибку: удалите соответствующий каталог кеша и повторите сборку.
Параллельные сборки из разных снимков исходников должны иметь разные `BUILD_DIR`:
при общем каталоге должны совпадать все staging-входы и корневые `go.mod`/`go.sum`.

ASM включается только при подтверждённой MIPS32r2 ISA. Init выбирает `auto`
для проверенной топологии MT7621 (два ядра, четыре VPE); остальные MIPS
используют Go. Переменные окружения позволяют отключить backend:

```sh
VLESS_MANAGER_MIPS_CHACHA20=generic
VLESS_MANAGER_MIPS_POLY1305=generic
```

`VLESS_MANAGER_MIPS_POLY1305=auto` включает Poly1305 на поддерживаемом CPU;
без этой переменной прямой запуск бинарника оставляет upstream Poly1305.

Native-проверки обязательны для изменения ASM: тесты на хосте не исполняют
MIPS-инструкции. Соберите тестовые бинарники и запустите на роутере из RAM:

```sh
go run ./packaging/mipscrypto -out build/mips-crypto
GOOS=linux GOARCH=mipsle GOMIPS=softfloat CGO_ENABLED=0 go test -modfile=build/mips-crypto/crypto.mod -c -o build/chacha.test golang.org/x/crypto/chacha20
GOOS=linux GOARCH=mipsle GOMIPS=softfloat CGO_ENABLED=0 go test -modfile=build/mips-crypto/crypto.mod -c -o build/aead.test golang.org/x/crypto/chacha20poly1305
GOOS=linux GOARCH=mipsle GOMIPS=softfloat CGO_ENABLED=0 go test -modfile=build/mips-crypto/crypto.mod -c -o build/poly.test golang.org/x/crypto/internal/poly1305
```

Каждый `/tmp/*.test` запускайте с `GOMEMLIMIT=16MiB`, затем повторите AEAD
с `VLESS_MANAGER_MIPS_CHACHA20=auto VLESS_MANAGER_MIPS_POLY1305=auto`.
Тесты проверяют RFC-векторы, совпадение с Go, выравнивание, границы страниц,
счётчики, изменение тегов и конкурентную работу с принудительным GC.
