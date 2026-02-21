# Bug: TLS_CERT/TLS_KEY 未設定時に Redfish サーバーが平文 HTTP にフォールバックする

**Date:** 2026-02-20
**File:** `cmd/qemu-bmc/main.go`
**Status:** Open
**Severity:** High（Rufio / bmclib など TLS を前提とする Redfish クライアントが接続不可）

## 症状

`TLS_CERT` / `TLS_KEY` 環境変数を設定せずに qemu-bmc を起動すると、Redfish サーバーが
ポート 443 で **平文 HTTP** を提供する。
`https://` で接続しようとするクライアントは SSL ハンドシェイクに失敗する。

```
curl: (35) error:0A00010B:SSL routines::wrong version number
```

Tinkerbell の Rufio が `bmc-machine.yaml` で `insecureTLS: true` を設定していても、
TLS ハンドシェイク自体が行われないため接続できない。

## 再現手順

```bash
# TLS_CERT / TLS_KEY を設定せずに起動（デフォルト状態）
docker run ghcr.io/tjst-t/qemu-bmc:latest

# HTTPS で接続を試みる
curl -vk https://<BMC_IP>/redfish/v1/
# → error:0A00010B:SSL routines::wrong version number

# 平文 HTTP では接続できてしまう
curl -v http://<BMC_IP>:443/redfish/v1/
# → HTTP/1.1 200 OK （平文で応答）
```

## 原因

`cmd/qemu-bmc/main.go` の else ブランチで、`TLSConfig` を設定しているにもかかわらず
`ListenAndServe`（平文 HTTP）を呼び出している。
`ListenAndServeTLS` を呼ばない限り TLS は有効にならない。

```go
// cmd/qemu-bmc/main.go
if cfg.TLSCert != "" && cfg.TLSKey != "" {
    // TLS_CERT / TLS_KEY あり → 正しく HTTPS
    httpServer.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
} else {
    // ← バグ: コメントは「自己署名証明書を生成する」と言っているが、
    //         実際は ListenAndServe（平文 HTTP）を呼んでいる
    log.Println("No TLS cert/key provided, generating self-signed certificate")
    tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
    httpServer.TLSConfig = tlsConfig  // TLSConfig を設定しても…
    httpServer.ListenAndServe()       // …これは平文 HTTP のまま
}
```

## 修正案

else ブランチで `crypto/tls` + `crypto/x509` を使って自己署名証明書を動的生成し、
`ListenAndServeTLS("", "")` に渡す。

```go
} else {
    cert, err := generateSelfSignedCert()
    if err != nil {
        log.Fatalf("Failed to generate self-signed cert: %v", err)
    }
    httpServer.TLSConfig = &tls.Config{
        Certificates: []tls.Certificate{cert},
        MinVersion:   tls.VersionTLS12,
    }
    if err := httpServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
        log.Fatalf("Redfish server error: %v", err)
    }
}
```

`generateSelfSignedCert` の実装例：

```go
import (
    "crypto/ecdsa"
    "crypto/elliptic"
    "crypto/rand"
    "crypto/tls"
    "crypto/x509"
    "crypto/x509/pkix"
    "encoding/pem"
    "math/big"
    "time"
)

func generateSelfSignedCert() (tls.Certificate, error) {
    key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
    if err != nil {
        return tls.Certificate{}, err
    }

    template := &x509.Certificate{
        SerialNumber: big.NewInt(1),
        Subject:      pkix.Name{CommonName: "qemu-bmc"},
        NotBefore:    time.Now(),
        NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
        KeyUsage:     x509.KeyUsageDigitalSignature,
        ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
    }

    certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
    if err != nil {
        return tls.Certificate{}, err
    }

    certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
    keyDER, err := x509.MarshalECPrivateKey(key)
    if err != nil {
        return tls.Certificate{}, err
    }
    keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

    return tls.X509KeyPair(certPEM, keyPEM)
}
```

## 影響範囲

| クライアント | 影響 |
|---|---|
| Rufio (Tinkerbell) | `insecureTLS: true` でも接続不可 |
| bmclib | TLS ハンドシェイク失敗 |
| `curl -k` (HTTPS) | `wrong version number` エラー |
| `curl` (HTTP) | 接続成功（平文） |

## 回避策

`TLS_CERT` と `TLS_KEY` を明示的に設定し、証明書ファイルをコンテナにマウントする。

```yaml
# topology YAML の env に追加
env:
  TLS_CERT: /certs/tls.crt
  TLS_KEY: /certs/tls.key
# binds で証明書ディレクトリをマウント
binds:
  - ./certs:/certs
```
